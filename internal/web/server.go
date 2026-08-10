package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"

	"github.com/zelenin/go-tdlib/client"
)

//go:embed static
var staticFiles embed.FS

const (
	authRateMax    = 10             // 每窗口内每个 IP 允许的认证请求数
	authRateWindow = time.Minute    // 限流窗口
	maxBodyBytes   = 1 << 20        // 请求体大小上限 1MB
)

type Server struct {
	webCfg      *config.Web
	appCfg      *config.Config
	auth        *WebAuthorizer
	httpSrv     *http.Server
	authLimiter *authRateLimiter

	userMu sync.RWMutex
	tdUser *client.User
}

func NewServer(webCfg *config.Web, appCfg *config.Config, auth *WebAuthorizer) *Server {
	return &Server{
		webCfg:      webCfg,
		appCfg:      appCfg,
		auth:        auth,
		authLimiter: newAuthRateLimiter(),
	}
}

func (s *Server) SetUser(user *client.User) {
	s.userMu.Lock()
	s.tdUser = user
	s.userMu.Unlock()
}

func (s *Server) currentUser() *client.User {
	s.userMu.RLock()
	defer s.userMu.RUnlock()
	return s.tdUser
}

// authReady 判断用户是否已完成登录。
// 注意：go-tdlib 的 Authorize 在 Ready 状态会短路返回，Handle 永远不会收到 Ready，
// 因此不能仅依赖 WebAuthorizer.IsReady()。以 SetUser 写入的登录用户为准。
func (s *Server) authReady() bool {
	if s.currentUser() != nil {
		return true
	}
	return s.auth != nil && s.auth.IsReady()
}

func (s *Server) Start() error {
	if s.webCfg == nil || !s.webCfg.Enable {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /login", s.handleIndex)
	mux.HandleFunc("GET /api/auth/state", s.handleAuthState)
	mux.HandleFunc("POST /api/auth/phone", s.handleAuthPhone)
	mux.HandleFunc("POST /api/auth/code", s.handleAuthCode)
	mux.HandleFunc("POST /api/auth/password", s.handleAuthPassword)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)

	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to load static files: %w", err)
	}
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))

	handler := s.withMiddleware(mux)

	port := s.webCfg.GetPort()
	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Infof("[Web] 管理面板已启动: http://0.0.0.0:%d", port)
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("[Web] HTTP 服务器错误: %v", err)
		}
	}()

	return nil
}

func (s *Server) Stop() {
	if s.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(ctx)
	}
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") {
			w.Header().Set("Content-Type", "application/json")

			if strings.HasPrefix(path, "/api/auth/") {
				// 登录相关端点必须可匿名访问，但需限制 POST 频率
				if r.Method == http.MethodPost && s.authLimiter != nil && !s.authLimiter.Allow(clientIP(r)) {
					writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
					return
				}
			} else if s.webCfg.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.webCfg.Token {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	content, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}

// ---- Auth API ----

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"state": "no_authorizer", "ready": false})
		return
	}

	state := s.auth.CurrentState()
	stateType := ""
	if state != nil {
		stateType = state.AuthorizationStateType()
	}

	resp := map[string]interface{}{
		"state": stateType,
		"ready": s.authReady(),
	}

	if errMsg := s.auth.LastError(); errMsg != "" {
		resp["error"] = errMsg
	}

	if state != nil {
		switch st := state.(type) {
		case *client.AuthorizationStateWaitOtherDeviceConfirmation:
			resp["link"] = st.Link
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type authPhoneRequest struct {
	PhoneNumber string `json:"phone_number"`
}

func (s *Server) handleAuthPhone(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authorizer not available"})
		return
	}
	var req authPhoneRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.PhoneNumber == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone_number is required"})
		return
	}
	s.auth.SetPhoneNumber(req.PhoneNumber)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type authCodeRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authorizer not available"})
		return
	}
	var req authCodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}
	s.auth.SetCode(req.Code)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type authPasswordRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authorizer not available"})
		return
	}
	var req authPasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password is required"})
		return
	}
	s.auth.SetPassword(req.Password)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Status API ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"auth_ready": s.authReady(),
		"config":     sanitizedConfig(s.appCfg),
	}

	if u := s.currentUser(); u != nil {
		resp["user"] = map[string]interface{}{
			"id":         u.Id,
			"first_name": u.FirstName,
			"last_name":  u.LastName,
			"username":   u.Usernames,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---- Config API ----

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sanitizedConfig(s.appCfg))
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// maskSecret 脱敏敏感字符串，保留首尾各 2 个字符用于识别。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= 6 {
		return "******"
	}
	return string(runes[:2]) + "****" + string(runes[len(runes)-2:])
}

// sanitizedConfig 返回配置的脱敏副本，避免 API 响应泄露密钥。
func sanitizedConfig(c *config.Config) *config.Config {
	if c == nil {
		return nil
	}
	out := *c
	out.TelegramApp.ApiHash = maskSecret(out.TelegramApp.ApiHash)
	out.LLM.APIKey = maskSecret(out.LLM.APIKey)
	out.LarkForward.AppSecret = maskSecret(out.LarkForward.AppSecret)
	out.Web.Token = maskSecret(out.Web.Token)
	return &out
}

// authRateLimiter 简单的每 IP 固定窗口限流器。
type authRateLimiter struct {
	mu   sync.Mutex
	hits map[string]*authRateBucket
}

type authRateBucket struct {
	count   int
	resetAt time.Time
}

func newAuthRateLimiter() *authRateLimiter {
	return &authRateLimiter{hits: make(map[string]*authRateBucket)}
}

func (l *authRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.hits[ip]
	if !ok || now.After(b.resetAt) {
		l.hits[ip] = &authRateBucket{count: 1, resetAt: now.Add(authRateWindow)}
		l.prune(now)
		return true
	}
	if b.count >= authRateMax {
		return false
	}
	b.count++
	return true
}

func (l *authRateLimiter) prune(now time.Time) {
	if len(l.hits) <= 1024 {
		return
	}
	for k, b := range l.hits {
		if now.After(b.resetAt) {
			delete(l.hits, k)
		}
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
