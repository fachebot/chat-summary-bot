package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/zelenin/go-tdlib/client"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc", "******"},
		{"12345678901234567890123456789012", "12****12"},
	}
	for _, c := range cases {
		if got := maskSecret(c.in); got != c.want {
			t.Errorf("maskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizedConfig(t *testing.T) {
	c := &config.Config{
		TelegramApp: config.TelegramApp{ApiId: 123, ApiHash: "hashhashhashhash"},
		LLM:         config.LLM{APIKey: "sk-secret-key", BaseURL: "https://x", Model: "m", MaxTokens: 100},
		LarkForward: config.LarkForward{AppSecret: "app-secret"},
		Web:         config.Web{Enable: true, Port: 8080, Token: "panel-token"},
	}
	out := sanitizedConfig(c)

	if strings.Contains(out.LLM.APIKey, "sk-secret-key") {
		t.Errorf("APIKey not masked: %q", out.LLM.APIKey)
	}
	if strings.Contains(out.TelegramApp.ApiHash, "hashhash") {
		t.Errorf("ApiHash not masked: %q", out.TelegramApp.ApiHash)
	}
	if strings.Contains(out.LarkForward.AppSecret, "app-secret") {
		t.Errorf("AppSecret not masked: %q", out.LarkForward.AppSecret)
	}
	if strings.Contains(out.Web.Token, "panel-token") {
		t.Errorf("Token not masked: %q", out.Web.Token)
	}
	if c.LLM.APIKey != "sk-secret-key" {
		t.Errorf("original config mutated")
	}
}

func TestAuthRateLimiter(t *testing.T) {
	l := newAuthRateLimiter()
	ip := "1.2.3.4"
	for i := 0; i < authRateMax; i++ {
		if !l.Allow(ip) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if l.Allow(ip) {
		t.Errorf("request beyond limit should be blocked")
	}
	if !l.Allow("5.6.7.8") {
		t.Errorf("different IP should be allowed")
	}
}

func TestWithMiddlewareTokenAuth(t *testing.T) {
	s := &Server{
		webCfg:      &config.Web{Token: "secret"},
		authLimiter: newAuthRateLimiter(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/auth/state", s.handleAuthState)
	mux.HandleFunc("POST /api/auth/token", s.handleAuthToken)
	handler := s.withMiddleware(mux)

	// /api/status 无 token → 401
	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", rec.Code)
	}

	// /api/status 带 token → 200
	req = httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with token, got %d", rec.Code)
	}

	// /api/auth/state 无 token → 401（Telegram 登录也在面板密码之后）
	req = httptest.NewRequest("GET", "/api/auth/state", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for auth endpoint without token, got %d", rec.Code)
	}

	// /api/auth/token 是面板登录入口，不要求 token
	req = httptest.NewRequest("POST", "/api/auth/token", bytes.NewBufferString(`{"token":"secret"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for token endpoint without auth header, got %d", rec.Code)
	}
}

func TestHandleAuthToken(t *testing.T) {
	s := &Server{webCfg: &config.Web{Token: "secret"}}

	req := httptest.NewRequest("POST", "/api/auth/token", bytes.NewBufferString(`{"token":"secret"}`))
	rec := httptest.NewRecorder()
	s.handleAuthToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for correct token, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/auth/token", bytes.NewBufferString(`{"token":"wrong"}`))
	rec = httptest.NewRecorder()
	s.handleAuthToken(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/auth/token", bytes.NewBufferString(`{"token":""}`))
	rec = httptest.NewRecorder()
	s.handleAuthToken(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty token, got %d", rec.Code)
	}
}

func TestWithMiddlewareAuthRateLimit(t *testing.T) {
	s := &Server{
		webCfg:      &config.Web{},
		auth:        NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
		authLimiter: newAuthRateLimiter(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/phone", s.handleAuthPhone)
	handler := s.withMiddleware(mux)

	body := `{"phone_number":"+8613800138000"}`
	for i := 0; i < authRateMax; i++ {
		req := httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("unexpected rate limit at request %d", i+1)
		}
	}
	req := httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after limit, got %d", rec.Code)
	}
}

func TestHandleAuthPhoneValidation(t *testing.T) {
	s := &Server{auth: NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil)}

	req := httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()
	s.handleAuthPhone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString(`{"phone_number":""}`))
	rec = httptest.NewRecorder()
	s.handleAuthPhone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty phone, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString(`{"phone_number":"+8613800138000"}`))
	rec = httptest.NewRecorder()
	s.handleAuthPhone(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid phone, got %d", rec.Code)
	}
}

func TestHandleStatusSanitizesConfig(t *testing.T) {
	s := &Server{
		appCfg: &config.Config{
			LLM: config.LLM{APIKey: "sk-top-secret", BaseURL: "x", Model: "m", MaxTokens: 100},
		},
	}
	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)

	var resp struct {
		Config *config.Config `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Config == nil {
		t.Fatal("config missing in status response")
	}
	if resp.Config.LLM.APIKey == "sk-top-secret" {
		t.Errorf("APIKey leaked in /api/status")
	}
}

func TestHandleAuthStateReadyAfterLogin(t *testing.T) {
	s := &Server{
		auth:   NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
		appCfg: &config.Config{},
	}

	// 未登录：ready 应为 false
	req := httptest.NewRequest("GET", "/api/auth/state", nil)
	rec := httptest.NewRecorder()
	s.handleAuthState(rec, req)
	var resp struct {
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Ready {
		t.Errorf("expected ready=false before login")
	}

	// 登录成功（SetUser 被 main.go 调用）后：ready 应为 true
	s.SetUser(&client.User{Id: 1, FirstName: "Tom"})
	req = httptest.NewRequest("GET", "/api/auth/state", nil)
	rec = httptest.NewRecorder()
	s.handleAuthState(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Ready {
		t.Errorf("expected ready=true after login (SetUser)")
	}
}

func TestHandleStatusUser(t *testing.T) {
	s := &Server{appCfg: &config.Config{}}
	s.SetUser(&client.User{Id: 123, FirstName: "Tom", LastName: "Cat"})

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)

	var resp struct {
		User map[string]interface{} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.User == nil {
		t.Fatal("user missing in status response")
	}
	if resp.User["first_name"] != "Tom" {
		t.Errorf("wrong first name: %v", resp.User["first_name"])
	}
}

func TestHandleAuthStatePhoneNumber(t *testing.T) {
	s := &Server{
		auth: NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
	}
	s.currentAuth().state = &client.AuthorizationStateWaitCode{
		CodeInfo: &client.AuthenticationCodeInfo{PhoneNumber: "+8613800138000"},
	}

	req := httptest.NewRequest("GET", "/api/auth/state", nil)
	rec := httptest.NewRecorder()
	s.handleAuthState(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["state"] != "authorizationStateWaitCode" {
		t.Errorf("state = %v", resp["state"])
	}
	if resp["phone_number"] != "+8613800138000" {
		t.Errorf("phone_number = %v, want +8613800138000", resp["phone_number"])
	}
}

func TestHandleProxyGet(t *testing.T) {
	s := &Server{appCfg: &config.Config{
		Sock5Proxy: config.Sock5Proxy{Enable: true, Type: "socks5", Host: "1.2.3.4", Port: 1080, Username: "u", Password: "secret"},
	}}

	req := httptest.NewRequest("GET", "/api/proxy", nil)
	rec := httptest.NewRecorder()
	s.handleProxyGet(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["host"] != "1.2.3.4" {
		t.Errorf("host = %v, want 1.2.3.4", resp["host"])
	}
	if resp["port"].(float64) != 1080 {
		t.Errorf("port = %v, want 1080", resp["port"])
	}
	if resp["type"] != "socks5" {
		t.Errorf("type = %v, want socks5", resp["type"])
	}
	if resp["password_set"] != true {
		t.Errorf("password_set = %v, want true", resp["password_set"])
	}
	if _, ok := resp["password"]; ok {
		t.Errorf("password value leaked in response")
	}
	if _, ok := resp["secret"]; ok {
		t.Errorf("secret value leaked in response")
	}
}

func TestHandleProxySetValidation(t *testing.T) {
	s := &Server{
		appCfg:     &config.Config{},
		auth:       NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
		configFile: filepath.Join(t.TempDir(), "config.yaml"),
	}

	// 启用但缺 host/port
	req := httptest.NewRequest("POST", "/api/proxy", bytes.NewBufferString(`{"enable":true,"type":"socks5","host":"","port":1080}`))
	rec := httptest.NewRecorder()
	s.handleProxySet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing host, got %d", rec.Code)
	}

	// 非法类型
	req = httptest.NewRequest("POST", "/api/proxy", bytes.NewBufferString(`{"enable":false,"type":"bad"}`))
	rec = httptest.NewRecorder()
	s.handleProxySet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad type, got %d", rec.Code)
	}
}

func TestHandleProxySetSuccess(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(f, []byte("Sock5Proxy:\n  Host: old\n  Port: 1080\n  Enable: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		appCfg:     &config.Config{},
		auth:       NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
		configFile: f,
	}

	// 提交新代理（含密码）
	body := `{"enable":true,"type":"socks5","host":"9.9.9.9","port":8080,"username":"u","password":"pw","password_set":false,"secret":"","secret_set":false}`
	req := httptest.NewRequest("POST", "/api/proxy", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	s.handleProxySet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 内存配置更新
	if !s.appCfg.Sock5Proxy.Enable || s.appCfg.Sock5Proxy.Host != "9.9.9.9" || s.appCfg.Sock5Proxy.Port != 8080 || s.appCfg.Sock5Proxy.Password != "pw" {
		t.Errorf("in-memory config not updated: %+v", s.appCfg.Sock5Proxy)
	}

	// 配置已写回文件
	data, _ := os.ReadFile(f)
	out := string(data)
	if !strings.Contains(out, "9.9.9.9") || !strings.Contains(out, "Enable: true") {
		t.Errorf("config file not updated: %s", out)
	}

	// 密码留空且 password_set=true 时保留旧值
	body = `{"enable":true,"type":"socks5","host":"9.9.9.9","port":8080,"username":"u","password":"","password_set":true,"secret":"","secret_set":false}`
	req = httptest.NewRequest("POST", "/api/proxy", bytes.NewBufferString(body))
	rec = httptest.NewRecorder()
	s.handleProxySet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if s.appCfg.Sock5Proxy.Password != "pw" {
		t.Errorf("password not preserved: %q", s.appCfg.Sock5Proxy.Password)
	}
}

func TestHandleStatusConnection(t *testing.T) {
	s := &Server{connState: func() string { return "connectionStateReady" }}

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)

	var resp struct {
		Connection struct {
			State string `json:"state"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Connection.State != "connectionStateReady" {
		t.Errorf("connection.state = %q, want connectionStateReady", resp.Connection.State)
	}
}

func TestHandleProxyStatusNotReady(t *testing.T) {
	s := &Server{
		appCfg: &config.Config{
			Sock5Proxy: config.Sock5Proxy{Enable: true, Type: "socks5", Host: "1.2.3.4", Port: 1080},
		},
		auth:      NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
		connState: func() string { return "connectionStateReady" },
	}

	req := httptest.NewRequest("GET", "/api/proxy/status", nil)
	rec := httptest.NewRecorder()
	s.handleProxyStatus(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["connection_state"] != "connectionStateReady" {
		t.Errorf("connection_state = %v, want connectionStateReady", resp["connection_state"])
	}
	cfg, ok := resp["configured"].(map[string]interface{})
	if !ok {
		t.Fatalf("configured missing: %v", resp)
	}
	if cfg["host"] != "1.2.3.4" {
		t.Errorf("configured.host = %v", cfg["host"])
	}
	if arr, ok := resp["proxies"].([]interface{}); !ok || len(arr) != 0 {
		t.Errorf("expected empty proxies when client not ready, got %v", resp["proxies"])
	}
	ping, ok := resp["ping"].(map[string]interface{})
	if !ok {
		t.Fatalf("ping missing: %v", resp)
	}
	if ping["ok"] != false {
		t.Errorf("expected ping ok=false when client not ready, got %v", ping["ok"])
	}
}

func TestHandleAuthPhoneRestart(t *testing.T) {
	s := &Server{
		appCfg: &config.Config{},
		auth:   NewWebAuthorizer(&client.SetTdlibParametersRequest{}, &config.Sock5Proxy{}, nil),
	}
	old := s.currentAuth()
	// 模拟已进入验证码阶段（用户返回后重新提交手机号）
	old.state = &client.AuthorizationStateWaitCode{}

	req := httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString(`{"phone_number":"+8613800138000"}`))
	rec := httptest.NewRecorder()
	s.handleAuthPhone(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if !s.IsRestarting() {
		t.Errorf("expected restarting=true after phone resubmit in WaitCode state")
	}

	newAuth := s.currentAuth()
	if newAuth == old {
		t.Errorf("authorizer not swapped on restart")
	}
	if newAuth == nil {
		t.Fatalf("new authorizer is nil")
	}

	// 新授权器应已缓冲新手机号
	phone, ok := newAuth.phone.Wait(make(chan struct{}))
	if !ok || phone != "+8613800138000" {
		t.Errorf("new phone not buffered: %q, %v", phone, ok)
	}

	// 若再次处于 WaitPhoneNumber 状态，则不应触发重启
	s.MarkRestartConsumed()
	s.currentAuth().state = &client.AuthorizationStateWaitPhoneNumber{}
	req = httptest.NewRequest("POST", "/api/auth/phone", bytes.NewBufferString(`{"phone_number":"+8613800138001"}`))
	rec = httptest.NewRecorder()
	s.handleAuthPhone(rec, req)
	if s.IsRestarting() {
		t.Errorf("expected no restart when state is WaitPhoneNumber")
	}
}

func TestPageRange(t *testing.T) {
	cases := []struct {
		total, page, pageSize int
		wantStart, wantEnd    int
	}{
		{100, 1, 20, 0, 20},
		{100, 5, 20, 80, 100},
		{100, 6, 20, 100, 100},
		{25, 2, 20, 20, 25},
		{0, 1, 20, 0, 0},
		{10, 0, 20, 0, 10},
		{10, 1, 0, 0, 10},
	}
	for _, c := range cases {
		start, end := pageRange(c.total, c.page, c.pageSize)
		if start != c.wantStart || end != c.wantEnd {
			t.Errorf("pageRange(%d,%d,%d) = (%d,%d), want (%d,%d)", c.total, c.page, c.pageSize, start, end, c.wantStart, c.wantEnd)
		}
	}
}

func TestHandleChatNotifyModeValidation(t *testing.T) {
	s := &Server{appCfg: &config.Config{}}

	// 非法 mode
	req := httptest.NewRequest("POST", "/api/chats/1001/notify-mode", bytes.NewBufferString(`{"mode":"bad"}`))
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	s.handleChatNotifyMode(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad mode, got %d", rec.Code)
	}

	// 非法 chat id
	req = httptest.NewRequest("POST", "/api/chats/abc/notify-mode", bytes.NewBufferString(`{"mode":"group"}`))
	req.SetPathValue("id", "abc")
	rec = httptest.NewRecorder()
	s.handleChatNotifyMode(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad chat id, got %d", rec.Code)
	}

	// 合法 mode，无 configFile（跳过持久化）
	req = httptest.NewRequest("POST", "/api/chats/1001/notify-mode", bytes.NewBufferString(`{"mode":"group"}`))
	req.SetPathValue("id", "1001")
	rec = httptest.NewRecorder()
	s.handleChatNotifyMode(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if s.appCfg.Summary.ChatNotifyModes[1001] != "group" {
		t.Errorf("in-memory mode not updated")
	}
	var resp struct {
		NotifyMode string `json:"notify_mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.NotifyMode != "group" {
		t.Errorf("notify_mode = %q, want group", resp.NotifyMode)
	}

	// 清除覆盖（mode 为空）
	req = httptest.NewRequest("POST", "/api/chats/1001/notify-mode", bytes.NewBufferString(`{"mode":""}`))
	req.SetPathValue("id", "1001")
	rec = httptest.NewRecorder()
	s.handleChatNotifyMode(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if _, ok := s.appCfg.Summary.ChatNotifyModes[1001]; ok {
		t.Errorf("override should be removed")
	}
}

func TestHandleChatNotifyModePersists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(f, []byte("Summary:\n  Cron: x\n  ChatNotifyModes:\n    -100: group\n  RetryTimes: 3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &Server{appCfg: &config.Config{Summary: config.Summary{ChatNotifyModes: map[int64]string{}}}, configFile: f}

	req := httptest.NewRequest("POST", "/api/chats/42/notify-mode", bytes.NewBufferString(`{"mode":"both"}`))
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()
	s.handleChatNotifyMode(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	data, _ := os.ReadFile(f)
	out := string(data)
	if !strings.Contains(out, "    42: both") {
		t.Errorf("notify mode not persisted: %s", out)
	}
	if strings.Contains(out, "-100: group") {
		t.Errorf("old entry not replaced")
	}
	if !strings.Contains(out, "RetryTimes: 3") {
		t.Errorf("sibling content lost")
	}
}
