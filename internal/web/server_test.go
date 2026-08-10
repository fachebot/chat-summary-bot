package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	handler := s.withMiddleware(mux)

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with token, got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/auth/state", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for auth endpoint without token, got %d", rec.Code)
	}
}

func TestWithMiddlewareAuthRateLimit(t *testing.T) {
	s := &Server{
		webCfg:      &config.Web{},
		auth:        NewWebAuthorizer(&client.SetTdlibParametersRequest{}),
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
	s := &Server{auth: NewWebAuthorizer(&client.SetTdlibParametersRequest{})}

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
		auth:   NewWebAuthorizer(&client.SetTdlibParametersRequest{}),
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
