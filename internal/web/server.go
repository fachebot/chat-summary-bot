package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/teleapp"

	"github.com/zelenin/go-tdlib/client"
)

//go:embed static
var staticFiles embed.FS

const (
	authRateMax    = 10          // 每窗口内每个 IP 允许的认证请求数
	authRateWindow = time.Minute // 限流窗口
	maxBodyBytes   = 1 << 20     // 请求体大小上限 1MB
)

type Server struct {
	webCfg      *config.Web
	appCfg      *config.Config
	configFile  string
	app         *teleapp.TeleApp
	httpSrv     *http.Server
	authLimiter *authRateLimiter
	connState   func() string

	authMu sync.RWMutex
	auth   *WebAuthorizer

	userMu  sync.RWMutex
	tdUser  *client.User
	proxyMu sync.Mutex
	cfgMu   sync.Mutex

	restartMu  sync.Mutex
	restarting bool
	authDone   <-chan struct{}
}

func NewServer(webCfg *config.Web, appCfg *config.Config, auth *WebAuthorizer, configFile string, connState func() string, app *teleapp.TeleApp) *Server {
	return &Server{
		webCfg:      webCfg,
		appCfg:      appCfg,
		configFile:  configFile,
		auth:        auth,
		authLimiter: newAuthRateLimiter(),
		connState:   connState,
		app:         app,
	}
}

// currentAuth 返回当前授权器（可能为 nil）。
func (s *Server) currentAuth() *WebAuthorizer {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	return s.auth
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
	if a := s.currentAuth(); a != nil {
		return a.IsReady()
	}
	return false
}

func (s *Server) Start() error {
	if s.webCfg == nil || !s.webCfg.Enable {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /login", s.handleIndex)
	mux.HandleFunc("GET /api/auth/state", s.handleAuthState)
	mux.HandleFunc("POST /api/auth/token", s.handleAuthToken)
	mux.HandleFunc("POST /api/auth/phone", s.handleAuthPhone)
	mux.HandleFunc("POST /api/auth/code", s.handleAuthCode)
	mux.HandleFunc("POST /api/auth/password", s.handleAuthPassword)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("GET /api/proxy", s.handleProxyGet)
	mux.HandleFunc("POST /api/proxy", s.handleProxySet)
	mux.HandleFunc("GET /api/proxy/status", s.handleProxyStatus)
	mux.HandleFunc("GET /api/chats", s.handleChatsList)
	mux.HandleFunc("GET /api/chats/{id}/summaries", s.handleChatSummaries)
	mux.HandleFunc("GET /api/chats/{id}/summary-dates", s.handleChatSummaryDates)
	mux.HandleFunc("POST /api/chats/{id}/notify-mode", s.handleChatNotifyMode)

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

	// 启动初始登录流程（与修改手机号重启走同一路径）
	if s.app != nil {
		done, err := s.app.LoginAsync(s.currentAuth())
		if err != nil {
			logger.Errorf("[Web] 启动登录流程失败: %v", err)
		} else {
			s.restartMu.Lock()
			s.authDone = done
			s.restartMu.Unlock()
		}
	}

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

			// 面板密码（Web.Token）门禁所有 API；/api/auth/token 是面板登录入口，除外
			if s.webCfg.Token != "" && path != "/api/auth/token" &&
				r.Header.Get("Authorization") != "Bearer "+s.webCfg.Token {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			// 登录相关 POST 端点限流（含面板密码登录 /api/auth/token）
			if strings.HasPrefix(path, "/api/auth/") && r.Method == http.MethodPost &&
				s.authLimiter != nil && !s.authLimiter.Allow(clientIP(r)) {
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
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
	auth := s.currentAuth()
	if auth == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"state": "no_authorizer", "ready": false})
		return
	}

	state := auth.CurrentState()
	stateType := ""
	if state != nil {
		stateType = state.AuthorizationStateType()
	}

	resp := map[string]interface{}{
		"state": stateType,
		"ready": s.authReady(),
	}

	if errMsg := auth.LastError(); errMsg != "" {
		resp["error"] = errMsg
	}

	if state != nil {
		switch st := state.(type) {
		case *client.AuthorizationStateWaitOtherDeviceConfirmation:
			resp["link"] = st.Link
		case *client.AuthorizationStateWaitCode:
			if st.CodeInfo != nil && st.CodeInfo.PhoneNumber != "" {
				resp["phone_number"] = st.CodeInfo.PhoneNumber
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type authTokenRequest struct {
	Token string `json:"token"`
}

// handleAuthToken 面板密码登录：校验 Web.Token。
func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	if s.webCfg == nil || s.webCfg.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token is not configured"})
		return
	}
	var req authTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token is required"})
		return
	}
	if req.Token != s.webCfg.Token {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type authPhoneRequest struct {
	PhoneNumber string `json:"phone_number"`
}

func (s *Server) handleAuthPhone(w http.ResponseWriter, r *http.Request) {
	auth := s.currentAuth()
	if auth == nil {
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

	// 若已进入验证码/密码阶段（用户返回后重新提交手机号），需重启登录流程以切换手机号。
	if st := auth.CurrentState(); st != nil && st.AuthorizationStateType() != client.TypeAuthorizationStateWaitPhoneNumber {
		s.restartLogin(req.PhoneNumber)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "restart": "true"})
		return
	}
	auth.SetPhoneNumber(req.PhoneNumber)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type authCodeRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	auth := s.currentAuth()
	if auth == nil {
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
	auth.SetCode(req.Code)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type authPasswordRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	auth := s.currentAuth()
	if auth == nil {
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
	auth.SetPassword(req.Password)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Status API ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"auth_ready": s.authReady(),
		"config":     sanitizedConfig(s.appCfg),
		"connection": map[string]interface{}{
			"state": s.connectionState(),
		},
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

// ---- Proxy API ----

type proxyAPIRequest struct {
	Enable      bool   `json:"enable"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	PasswordSet bool   `json:"password_set"`
	Secret      string `json:"secret"`
	SecretSet   bool   `json:"secret_set"`
}

func (s *Server) handleProxyGet(w http.ResponseWriter, r *http.Request) {
	s.proxyMu.Lock()
	var p config.Sock5Proxy
	if s.appCfg != nil {
		p = s.appCfg.Sock5Proxy
	}
	s.proxyMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enable":       p.Enable,
		"type":         p.ProxyType(),
		"host":         p.Host,
		"port":         p.Port,
		"username":     p.Username,
		"password_set": p.Password != "",
		"secret_set":   p.Secret != "",
	})
}

func (s *Server) handleProxySet(w http.ResponseWriter, r *http.Request) {
	auth := s.currentAuth()
	if s.appCfg == nil || auth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service not ready"})
		return
	}

	var req proxyAPIRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	s.proxyMu.Lock()
	current := s.appCfg.Sock5Proxy
	s.proxyMu.Unlock()

	cfg := config.Sock5Proxy{
		Enable:   req.Enable,
		Type:     req.Type,
		Host:     strings.TrimSpace(req.Host),
		Port:     req.Port,
		Username: req.Username,
		Password: req.Password,
		Secret:   req.Secret,
	}
	// 密码/secret 留空且原本已设置时，保留旧值
	if cfg.Password == "" && req.PasswordSet {
		cfg.Password = current.Password
	}
	if cfg.Secret == "" && req.SecretSet {
		cfg.Secret = current.Secret
	}

	switch cfg.ProxyType() {
	case "socks5", "http", "mtproto":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type 必须是 socks5/http/mtproto"})
		return
	}
	if cfg.Enable && (cfg.Host == "" || cfg.Port <= 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "启用代理时 host 和 port 必填"})
		return
	}

	// 更新内存配置并同步 tdlib（清旧 + 加新）
	s.proxyMu.Lock()
	s.appCfg.Sock5Proxy = cfg
	s.proxyMu.Unlock()
	auth.SetProxyConfig(cfg)

	if err := auth.SyncProxyNow(); err != nil {
		logger.Errorf("[Web] 应用代理失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "代理应用失败: " + err.Error()})
		return
	}

	// 持久化到配置文件，重启后恢复
	if s.configFile != "" {
		if err := config.SaveProxy(s.configFile, &cfg); err != nil {
			logger.Errorf("[Web] 保存代理配置失败: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "代理已应用但保存配置文件失败: " + err.Error()})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// connectionState 返回最近一次 TDLib 连接状态，未接入时为 ""。
func (s *Server) connectionState() string {
	if s.connState != nil {
		return s.connState()
	}
	return ""
}

// ---- Proxy Status API ----

// proxyPingTimeout 是 pingProxy 的最长等待时间，避免断网时接口长时间阻塞。
const proxyPingTimeout = 10 * time.Second

// handleProxyStatus 返回代理的连接状态：已配置代理、tdlib 当前代理列表、
// 连接状态，以及对启用代理（无代理则直连）的实测延迟。
func (s *Server) handleProxyStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"connection_state": s.connectionState(),
	}

	// 已配置代理
	s.proxyMu.Lock()
	var configured config.Sock5Proxy
	if s.appCfg != nil {
		configured = s.appCfg.Sock5Proxy
	}
	s.proxyMu.Unlock()
	resp["configured"] = map[string]interface{}{
		"enable":       configured.Enable,
		"type":         configured.ProxyType(),
		"host":         configured.Host,
		"port":         configured.Port,
		"password_set": configured.Password != "",
	}

	// tdlib 当前代理列表 + 启用的代理
	var proxies []map[string]interface{}
	enabledID := int32(0)
	if c := s.tdlibClient(); c != nil {
		if list, err := c.GetProxies(); err == nil {
			for _, p := range list.Proxies {
				proxies = append(proxies, map[string]interface{}{
					"id":             p.Id,
					"server":         p.Server,
					"port":           p.Port,
					"is_enabled":     p.IsEnabled,
					"last_used_date": p.LastUsedDate,
				})
				if p.IsEnabled {
					enabledID = p.Id
				}
			}
		}
	}
	if proxies == nil {
		proxies = []map[string]interface{}{}
	}
	resp["proxies"] = proxies

	// 实测连通性：有启用代理则 ping 代理，否则直连
	resp["ping"] = s.pingProxy(enabledID)

	writeJSON(w, http.StatusOK, resp)
}

// pingProxy 调用 tdlib pingProxy，结果受 proxyPingTimeout 限制。
// proxyID 为 0 时直连 Telegram 服务器（不带代理）。
func (s *Server) pingProxy(proxyID int32) map[string]interface{} {
	c := s.tdlibClient()
	if c == nil {
		return map[string]interface{}{"ok": false, "error": "tdlib 客户端未就绪"}
	}

	type result struct {
		seconds float64
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		res, err := c.PingProxy(&client.PingProxyRequest{ProxyId: proxyID})
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{seconds: res.Seconds}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return map[string]interface{}{"ok": false, "error": r.err.Error()}
		}
		return map[string]interface{}{"ok": true, "seconds": r.seconds}
	case <-time.After(proxyPingTimeout):
		return map[string]interface{}{"ok": false, "error": "检测超时"}
	}
}

// tdlibClient 返回当前 tdlib 客户端（可能为 nil）。
func (s *Server) tdlibClient() *client.Client {
	auth := s.currentAuth()
	if auth == nil {
		return nil
	}
	return auth.Client()
}

// ---- Chats API ----

// notifyModeFor 返回指定群聊的生效通知方式。
func (s *Server) notifyModeFor(chatID int64) string {
	if s.appCfg == nil {
		return ""
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.appCfg.Summary.GetNotifyMode(chatID)
}

func parseChatID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid chat id"})
		return 0, false
	}
	return id, true
}

// handleChatsList 返回群聊列表（分页）。
func (s *Server) handleChatsList(w http.ResponseWriter, r *http.Request) {
	if s.app == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service not ready"})
		return
	}

	page, pageSize := 1, 20
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			pageSize = n
		}
	}

	chats, err := s.app.ListGroupChats()
	if err != nil {
		logger.Errorf("[Web] 获取群聊列表失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "获取群聊列表失败"})
		return
	}

	total := len(chats)
	start, end := pageRange(total, page, pageSize)

	items := make([]map[string]interface{}, 0, end-start)
	for _, c := range chats[start:end] {
		items = append(items, map[string]interface{}{
			"id":          c.ID,
			"title":       c.Title,
			"notify_mode": s.notifyModeFor(c.ID),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chats":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// pageRange 计算分页切片区间 [start, end)。
func pageRange(total, page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return start, end
}

// handleChatSummaries 返回指定群聊指定日期的摘要。
func (s *Server) handleChatSummaries(w http.ResponseWriter, r *http.Request) {
	if s.app == nil || s.app.SummaryModel() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service not ready"})
		return
	}
	chatID, ok := parseChatID(w, r)
	if !ok {
		return
	}
	dateStr := r.URL.Query().Get("date")
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date 格式应为 YYYY-MM-DD"})
		return
	}

	items, err := s.app.SummaryModel().GetByDateAndChat(r.Context(), chatID, date)
	if err != nil {
		logger.Errorf("[Web] 查询摘要失败 chat=%d date=%s: %v", chatID, dateStr, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询摘要失败"})
		return
	}

	list := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		list = append(list, map[string]interface{}{
			"sender_name": it.SenderName,
			"content":     it.Content,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chat_id": chatID,
		"date":    dateStr,
		"items":   list,
	})
}

// handleChatSummaryDates 返回指定群聊有摘要的日期列表。
func (s *Server) handleChatSummaryDates(w http.ResponseWriter, r *http.Request) {
	if s.app == nil || s.app.SummaryModel() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service not ready"})
		return
	}
	chatID, ok := parseChatID(w, r)
	if !ok {
		return
	}
	dates, err := s.app.SummaryModel().GetDailySummaryDates(r.Context(), chatID)
	if err != nil {
		logger.Errorf("[Web] 查询摘要日期失败 chat=%d: %v", chatID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询摘要日期失败"})
		return
	}
	dateStrs := make([]string, 0, len(dates))
	for _, d := range dates {
		dateStrs = append(dateStrs, d.Format("2006-01-02"))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"dates": dateStrs})
}

// handleChatNotifyMode 设置指定群聊的通知方式（写入 Config.Summary.ChatNotifyModes）。
func (s *Server) handleChatNotifyMode(w http.ResponseWriter, r *http.Request) {
	if s.appCfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service not ready"})
		return
	}
	chatID, ok := parseChatID(w, r)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	switch req.Mode {
	case "private", "group", "both", "":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode 必须是 private/group/both 或空"})
		return
	}

	s.cfgMu.Lock()
	if s.appCfg.Summary.ChatNotifyModes == nil {
		s.appCfg.Summary.ChatNotifyModes = make(map[int64]string)
	}
	if req.Mode == "" {
		delete(s.appCfg.Summary.ChatNotifyModes, chatID)
	} else {
		s.appCfg.Summary.ChatNotifyModes[chatID] = req.Mode
	}
	modes := make(map[int64]string, len(s.appCfg.Summary.ChatNotifyModes))
	for k, v := range s.appCfg.Summary.ChatNotifyModes {
		modes[k] = v
	}
	effective := s.appCfg.Summary.GetNotifyMode(chatID)
	s.cfgMu.Unlock()

	if s.configFile != "" {
		if err := config.SaveChatNotifyModes(s.configFile, modes); err != nil {
			logger.Errorf("[Web] 保存通知方式配置失败: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "通知方式已生效但保存配置文件失败: " + err.Error()})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "notify_mode": effective})
}

// ---- Restart Login (修改手机号) ----

// restartLogin 中止当前登录流程并用新手机号重新发起登录。
// 用于用户在验证码/密码阶段返回修改手机号后重新提交的情况。
func (s *Server) restartLogin(phone string) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	s.restarting = true
	logger.Infof("[Web] 重启登录流程，新手机号: %s", phone)

	// 中止当前授权流程：用 TDLib 自带的 destroy 清理本地数据（含持久化的旧 WaitCode），
	// 避免手动删文件与 TDLib 的 binlog 写入竞争；然后关闭 done 唤醒旧 Handle。
	// 不要主动调用旧客户端的 Close()——旧 Authorize 循环自己会关闭它。
	old := s.currentAuth()
	if old != nil {
		if c := old.Client(); c != nil {
			if _, err := c.Destroy(); err != nil {
				logger.Warnf("[Web] 销毁旧客户端失败: %v", err)
			} else {
				logger.Infof("[Web] 已销毁旧客户端，本地数据已清理")
			}
		}
		old.Close()
	}
	s.authDone = nil

	// Destroy 清除了本地数据目录，重新创建，避免新客户端 binlog 初始化报错
	if s.app != nil {
		teleapp.EnsureTdlibDirs(s.app.TdlibParameters())
	}

	// 重置更新监听，允许新客户端重新挂载并捕获连接状态
	if s.app != nil {
		s.app.ResetUpdates()
	}

	// 新建授权器并替换
	newAuth := s.newAuthorizer()
	s.authMu.Lock()
	s.auth = newAuth
	s.authMu.Unlock()

	if s.app != nil {
		done, err := s.app.LoginAsync(newAuth)
		if err != nil {
			logger.Errorf("[Web] 重启登录流程失败: %v", err)
		}
		s.authDone = done
	}
	// 预先设置新手机号：新客户端到 WaitPhoneNumber 时立即消费
	newAuth.SetPhoneNumber(phone)
	logger.Infof("[Web] 重启登录流程已启动")
}

// newAuthorizer 基于当前配置创建一个新的 WebAuthorizer。
func (s *Server) newAuthorizer() *WebAuthorizer {
	var params *client.SetTdlibParametersRequest
	var onClientReady func(*client.Client)
	if s.app != nil {
		params = s.app.TdlibParameters()
		onClientReady = s.app.StartUpdates
	}
	s.proxyMu.Lock()
	cfg := config.Sock5Proxy{}
	if s.appCfg != nil {
		cfg = s.appCfg.Sock5Proxy
	}
	s.proxyMu.Unlock()
	return NewWebAuthorizer(params, &cfg, onClientReady)
}

// IsRestarting 判断登录流程是否处于"重启中"，供 main 的等待循环判断。
func (s *Server) IsRestarting() bool {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.restarting
}

// MarkRestartConsumed 消费重启标记。
func (s *Server) MarkRestartConsumed() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.restarting = false
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
	out.Sock5Proxy.Password = maskSecret(out.Sock5Proxy.Password)
	out.Sock5Proxy.Secret = maskSecret(out.Sock5Proxy.Secret)
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
