package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyType(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "socks5"},
		{"SOCKS5", "socks5"},
		{"http", "http"},
		{"mtproto", "mtproto"},
		{"  HTTP  ", "http"},
	}
	for _, c := range cases {
		p := Sock5Proxy{Type: c.in}
		if got := p.ProxyType(); got != c.want {
			t.Errorf("ProxyType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsSOCKS5(t *testing.T) {
	if !(Sock5Proxy{}).IsSOCKS5() {
		t.Errorf("empty type should default to socks5")
	}
	if !(Sock5Proxy{Type: "socks5"}).IsSOCKS5() {
		t.Errorf("socks5 should be true")
	}
	if (Sock5Proxy{Type: "http"}).IsSOCKS5() {
		t.Errorf("http should be false")
	}
}

func validBaseConfig() Config {
	return Config{
		TelegramApp: TelegramApp{ApiId: 1, ApiHash: "h"},
		LLM:         LLM{APIKey: "k", BaseURL: "b", Model: "m", MaxTokens: 10, MaxOutputTokens: 0},
		Summary:     Summary{Cron: "0 0 * * *", NotifyMode: "private", NotifyUserIds: []int64{1}},
	}
}

func TestValidateProxy(t *testing.T) {
	// 合法配置不应报代理相关错误
	ok := validBaseConfig()
	ok.Sock5Proxy = Sock5Proxy{Enable: true, Type: "http", Host: "1.2.3.4", Port: 8080}
	if err := ok.Validate(); err != nil && strings.Contains(err.Error(), "Sock5Proxy") {
		t.Errorf("unexpected proxy error: %v", err)
	}

	// 非法类型
	bad := validBaseConfig()
	bad.Sock5Proxy = Sock5Proxy{Enable: false, Type: "foo"}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "Sock5Proxy.Type") {
		t.Errorf("expected Type error, got: %v", err)
	}

	// 启用但 Host 为空
	bad = validBaseConfig()
	bad.Sock5Proxy = Sock5Proxy{Enable: true, Host: "", Port: 1080}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "Sock5Proxy.Host") {
		t.Errorf("expected Host error, got: %v", err)
	}

	// 启用但 Port 非法
	bad = validBaseConfig()
	bad.Sock5Proxy = Sock5Proxy{Enable: true, Host: "x", Port: 0}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "Sock5Proxy.Port") {
		t.Errorf("expected Port error, got: %v", err)
	}
}

func TestSaveProxyUpdatesBlock(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	content := `# 代理服务器配置
Sock5Proxy:
  Host: 127.0.0.1 # 代理服务器地址
  Port: 1080 # 代理服务器端口
  Enable: true # 是否启用代理

# 电报App配置
TelegramApp:
  ApiId: 1
  ApiHash: abc
`
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	p := Sock5Proxy{Enable: false, Type: "http", Host: "1.2.3.4", Port: 8080, Username: "u", Password: "p@ss"}
	if err := SaveProxy(f, &p); err != nil {
		t.Fatalf("SaveProxy: %v", err)
	}

	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// 其他部分与注释保留
	for _, want := range []string{"# 代理服务器配置", "# 电报App配置", "TelegramApp:", "ApiId: 1", "ApiHash: abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost content: %q", want)
		}
	}
	// 新块内容
	for _, want := range []string{`Host: "1.2.3.4"`, "Port: 8080", "Enable: false", "Type: http", `Username: "u"`, `Password: "p@ss"`} {
		if !strings.Contains(out, want) {
			t.Errorf("block not updated, missing %q", want)
		}
	}
	if strings.Contains(out, "127.0.0.1") {
		t.Errorf("old host not removed")
	}
}

func TestSaveProxyPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	content := "# c1\r\nSock5Proxy:\r\n  Host: 1.1.1.1\r\n  Port: 1080\r\n  Enable: true\r\n# c2\r\nTelegramApp:\r\n  ApiId: 1\r\n"
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SaveProxy(f, &Sock5Proxy{Enable: true, Host: "2.2.2.2", Port: 1080}); err != nil {
		t.Fatalf("SaveProxy: %v", err)
	}

	data, _ := os.ReadFile(f)
	out := string(data)
	if !strings.Contains(out, "\r\n") {
		t.Errorf("CRLF lost")
	}
	if strings.Contains(out, "1.1.1.1") {
		t.Errorf("old host not removed")
	}
	if !strings.Contains(out, "2.2.2.2") {
		t.Errorf("new host not written")
	}
	if !strings.Contains(out, "TelegramApp:") {
		t.Errorf("rest lost")
	}
}

func TestSaveProxyAppendsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	content := "# only\nTelegramApp:\n  ApiId: 1\n"
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SaveProxy(f, &Sock5Proxy{Enable: true, Host: "x", Port: 1}); err != nil {
		t.Fatalf("SaveProxy: %v", err)
	}

	data, _ := os.ReadFile(f)
	out := string(data)
	if !strings.Contains(out, "Sock5Proxy:") {
		t.Errorf("Sock5Proxy block not appended")
	}
	if !strings.Contains(out, "TelegramApp:") {
		t.Errorf("existing content lost")
	}
}
