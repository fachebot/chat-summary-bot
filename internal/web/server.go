package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"

	"github.com/zelenin/go-tdlib/client"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	webCfg     *config.Web
	appCfg     *config.Config
	tdClient   *client.Client
	tdUser     *client.User
	auth       *WebAuthorizer
	configFile string
	httpSrv    *http.Server
}

func NewServer(webCfg *config.Web, appCfg *config.Config, tdClient *client.Client, tdUser *client.User, auth *WebAuthorizer, configFile string) *Server {
	return &Server{
		webCfg:     webCfg,
		appCfg:     appCfg,
		tdClient:   tdClient,
		tdUser:     tdUser,
		auth:       auth,
		configFile: configFile,
	}
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
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
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
		// API 路由 auth token 校验
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			if len(r.URL.Path) < 10 || r.URL.Path[:10] != "/api/auth/" {
				if s.webCfg.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.webCfg.Token {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
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
	w.Write(content)
}

// ---- Auth API ----

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"state": "no_authorizer", "ready": false})
		return
	}

	state := s.auth.CurrentState()
	stateType := ""
	if state != nil {
		stateType = state.AuthorizationStateType()
	}

	resp := map[string]interface{}{
		"state": stateType,
		"ready": s.auth.IsReady(),
	}

	if state != nil {
		switch st := state.(type) {
		case *client.AuthorizationStateWaitOtherDeviceConfirmation:
			resp["link"] = st.Link
		}
	}

	json.NewEncoder(w).Encode(resp)
}

type authPhoneRequest struct {
	PhoneNumber string `json:"phone_number"`
}

func (s *Server) handleAuthPhone(w http.ResponseWriter, r *http.Request) {
	var req authPhoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}
	if req.PhoneNumber == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "phone_number is required"})
		return
	}
	s.auth.SetPhoneNumber(req.PhoneNumber)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type authCodeRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	var req authCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}
	if req.Code == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "code is required"})
		return
	}
	s.auth.SetCode(req.Code)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type authPasswordRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	var req authPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}
	if req.Password == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "password is required"})
		return
	}
	s.auth.SetPassword(req.Password)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---- Status API ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"auth_ready": s.auth != nil && s.auth.IsReady(),
		"config":     s.appCfg,
	}

	if s.tdUser != nil {
		resp["user"] = map[string]interface{}{
			"id":         s.tdUser.Id,
			"first_name": s.tdUser.FirstName,
			"last_name":  s.tdUser.LastName,
			"username":   s.tdUser.Usernames,
		}
	}

	json.NewEncoder(w).Encode(resp)
}

// ---- Config API ----

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(s.appCfg)
}

// ---- Logs ----

func readLogFile(limit int) []string {
	data, err := os.ReadFile("logs/chat-summary.log")
	if err != nil {
		return []string{fmt.Sprintf("log file not available: %v", err)}
	}
	// Return last N lines
	lines := []string{}
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}
