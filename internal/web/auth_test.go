package web

import (
	"errors"
	"testing"
	"time"

	"github.com/zelenin/go-tdlib/client"
)

func TestAuthInputSetBeforeWait(t *testing.T) {
	in := newAuthInput()
	in.Set("123")
	v, ok := in.Wait(make(chan struct{}))
	if !ok || v != "123" {
		t.Errorf("Wait after Set = (%q, %v), want (\"123\", true)", v, ok)
	}
}

func TestAuthInputWaitThenSet(t *testing.T) {
	in := newAuthInput()
	done := make(chan struct{})
	got := make(chan string, 1)
	go func() {
		v, _ := in.Wait(done)
		got <- v
	}()

	select {
	case <-got:
		t.Fatalf("Wait returned before Set")
	case <-time.After(20 * time.Millisecond):
	}

	in.Set("abc")
	select {
	case v := <-got:
		if v != "abc" {
			t.Errorf("got %q, want \"abc\"", v)
		}
	case <-time.After(time.Second):
		t.Fatalf("Wait did not return after Set")
	}
}

func TestAuthInputWaitCancelledByDone(t *testing.T) {
	in := newAuthInput()
	done := make(chan struct{})
	closed := make(chan bool, 1)
	go func() {
		_, ok := in.Wait(done)
		closed <- ok
	}()

	close(done)
	select {
	case ok := <-closed:
		if ok {
			t.Errorf("Wait should return false when done closed")
		}
	case <-time.After(time.Second):
		t.Fatalf("Wait did not return after done closed")
	}
}

func TestAuthInputSetNonBlocking(t *testing.T) {
	in := newAuthInput()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			in.Set("x")
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Set should never block")
	}

	v, ok := in.Wait(make(chan struct{}))
	if !ok || v != "x" {
		t.Errorf("Wait after Sets = (%q, %v), want (\"x\", true)", v, ok)
	}
}

func TestFriendlyAuthErrorMappings(t *testing.T) {
	cases := []struct {
		code    int32
		message string
		want    string
	}{
		{400, "PHONE_CODE_INVALID", "验证码错误，请检查后重试"},
		{400, "PHONE_CODE_EXPIRED", "验证码已过期，请重新获取"},
		{400, "PHONE_NUMBER_INVALID", "手机号格式不正确，请检查后重试"},
		{429, "FLOOD_WAIT_60", "操作过于频繁，请稍后再试"},
		{400, "PASSWORD_HASH_INVALID", "两步验证密码错误，请重试"},
		{400, "AUTH_KEY_UNREGISTERED", "登录会话已失效，请重新登录"},
		{400, "CONNECTION_NOT_INITED", "无法连接到 Telegram，请检查网络或代理设置"},
		{400, "PROXY_NOT_FOUND", "代理连接失败，请检查代理设置"},
		{500, "SOME_UNKNOWN_ERROR", "SOME_UNKNOWN_ERROR"},
		{400, "", "Telegram 错误码: 400"},
	}
	for _, c := range cases {
		err := client.ResponseError{Err: &client.Error{Code: c.code, Message: c.message}}
		if got := friendlyAuthError(err); got != c.want {
			t.Errorf("friendlyAuthError(%q) = %q, want %q", c.message, got, c.want)
		}
	}
}

func TestFriendlyAuthErrorNonResponseError(t *testing.T) {
	// 非 ResponseError（如 Send 超时）也应给出友好提示
	err := errors.New("response catching timeout")
	if got := friendlyAuthError(err); got != "连接超时，请检查网络或代理设置" {
		t.Errorf("friendlyAuthError(timeout) = %q", got)
	}

	err = errors.New("boom")
	if got := friendlyAuthError(err); got != "boom" {
		t.Errorf("friendlyAuthError(raw) = %q, want boom", got)
	}
}
