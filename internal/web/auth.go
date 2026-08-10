package web

import (
	"fmt"
	"sync"

	"github.com/zelenin/go-tdlib/client"
)

// authInput 保存等待中的登录输入（手机号/验证码/两步验证密码）。
// Set 是非阻塞操作，不会挂起 HTTP handler；Wait 由授权状态机阻塞调用。
type authInput struct {
	mu     sync.Mutex
	value  string
	have   bool
	notify chan struct{}
}

func newAuthInput() *authInput {
	return &authInput{notify: make(chan struct{}, 1)}
}

// Set 保存最新输入值并唤醒等待方，永远不会阻塞。
func (a *authInput) Set(v string) {
	a.mu.Lock()
	a.value = v
	a.have = true
	a.mu.Unlock()

	select {
	case a.notify <- struct{}{}:
	default:
	}
}

// Wait 阻塞直到获得一个新值或 done 关闭，返回 (值, 是否成功)。
func (a *authInput) Wait(done <-chan struct{}) (string, bool) {
	for {
		a.mu.Lock()
		if a.have {
			v := a.value
			a.have = false
			a.mu.Unlock()
			return v, true
		}
		a.mu.Unlock()

		select {
		case <-a.notify:
		case <-done:
			return "", false
		}
	}
}

// WebAuthorizer 通过 Web 面板完成 Telegram 授权。
type WebAuthorizer struct {
	mu        sync.Mutex
	lastErr   string
	params    *client.SetTdlibParametersRequest
	state     client.AuthorizationState
	phone     *authInput
	code      *authInput
	password  *authInput
	done      chan struct{}
	closeOnce sync.Once
}

func NewWebAuthorizer(params *client.SetTdlibParametersRequest) *WebAuthorizer {
	return &WebAuthorizer{
		params:   params,
		phone:    newAuthInput(),
		code:     newAuthInput(),
		password: newAuthInput(),
		done:     make(chan struct{}),
	}
}

func (w *WebAuthorizer) Handle(c *client.Client, state client.AuthorizationState) error {
	w.mu.Lock()
	w.state = state
	w.mu.Unlock()

	switch state.AuthorizationStateType() {
	case client.TypeAuthorizationStateWaitTdlibParameters:
		_, err := c.SetTdlibParameters(w.params)
		return err

	case client.TypeAuthorizationStateWaitPhoneNumber:
		phone, ok := w.phone.Wait(w.done)
		if !ok {
			return fmt.Errorf("authorization cancelled")
		}
		_, err := c.SetAuthenticationPhoneNumber(&client.SetAuthenticationPhoneNumberRequest{
			PhoneNumber: phone,
			Settings: &client.PhoneNumberAuthenticationSettings{
				AllowFlashCall:       false,
				IsCurrentPhoneNumber: false,
				AllowSmsRetrieverApi: false,
			},
		})
		if err != nil {
			// 返回 nil 避免库触发 client.Close() 杀掉进程，允许用户重试
			w.setError(fmt.Errorf("提交手机号失败: %w", err))
			return nil
		}
		w.setError(nil)
		return nil

	case client.TypeAuthorizationStateWaitCode:
		code, ok := w.code.Wait(w.done)
		if !ok {
			return fmt.Errorf("authorization cancelled")
		}
		_, err := c.CheckAuthenticationCode(&client.CheckAuthenticationCodeRequest{
			Code: code,
		})
		if err != nil {
			w.setError(fmt.Errorf("验证码错误: %w", err))
			return nil
		}
		w.setError(nil)
		return nil

	case client.TypeAuthorizationStateWaitPassword:
		password, ok := w.password.Wait(w.done)
		if !ok {
			return fmt.Errorf("authorization cancelled")
		}
		_, err := c.CheckAuthenticationPassword(&client.CheckAuthenticationPasswordRequest{
			Password: password,
		})
		if err != nil {
			w.setError(fmt.Errorf("两步验证密码错误: %w", err))
			return nil
		}
		w.setError(nil)
		return nil

	case client.TypeAuthorizationStateWaitOtherDeviceConfirmation:
		// 状态通过 CurrentState 暴露给前端展示确认链接
		return nil

	case client.TypeAuthorizationStateReady:
		w.setError(nil)
		w.closeOnce.Do(func() { close(w.done) })
		return nil

	case client.TypeAuthorizationStateClosing, client.TypeAuthorizationStateClosed, client.TypeAuthorizationStateLoggingOut:
		return nil

	default:
		return client.NotSupportedAuthorizationState(state)
	}
}

func (w *WebAuthorizer) Close() {
	w.closeOnce.Do(func() { close(w.done) })
}

func (w *WebAuthorizer) CurrentState() client.AuthorizationState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *WebAuthorizer) IsReady() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state != nil && w.state.AuthorizationStateType() == client.TypeAuthorizationStateReady
}

func (w *WebAuthorizer) SetPhoneNumber(phone string) {
	w.setError(nil)
	w.phone.Set(phone)
}

func (w *WebAuthorizer) SetCode(code string) {
	w.setError(nil)
	w.code.Set(code)
}

func (w *WebAuthorizer) SetPassword(password string) {
	w.setError(nil)
	w.password.Set(password)
}

func (w *WebAuthorizer) setError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.lastErr = err.Error()
	} else {
		w.lastErr = ""
	}
}

func (w *WebAuthorizer) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}
