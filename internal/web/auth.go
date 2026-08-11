package web

import (
	"fmt"
	"strings"
	"sync"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/teleapp"

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
	mu            sync.Mutex
	lastErr       string
	params        *client.SetTdlibParametersRequest
	state         client.AuthorizationState
	tdlibClient   *client.Client
	phone         *authInput
	code          *authInput
	password      *authInput
	done          chan struct{}
	closeOnce     sync.Once
	onClientReady func(*client.Client)

	proxyMu  sync.Mutex
	proxyCfg config.Sock5Proxy
}

// NewWebAuthorizer 创建 Web 授权器。onClientReady 在 tdlib 客户端初始化完成后回调，
// 用于尽早挂载更新监听以捕获连接状态。
func NewWebAuthorizer(params *client.SetTdlibParametersRequest, proxyCfg *config.Sock5Proxy, onClientReady func(*client.Client)) *WebAuthorizer {
	cfg := config.Sock5Proxy{}
	if proxyCfg != nil {
		cfg = *proxyCfg
	}
	return &WebAuthorizer{
		params:        params,
		phone:         newAuthInput(),
		code:          newAuthInput(),
		password:      newAuthInput(),
		done:          make(chan struct{}),
		proxyCfg:      cfg,
		onClientReady: onClientReady,
	}
}

func (w *WebAuthorizer) Handle(c *client.Client, state client.AuthorizationState) error {
	w.mu.Lock()
	w.state = state
	w.mu.Unlock()

	switch state.AuthorizationStateType() {
	case client.TypeAuthorizationStateWaitTdlibParameters:
		logger.Infof("[Web] 授权状态: WaitTdlibParameters，初始化客户端")
		// 保存客户端引用供 Web 代理同步使用。
		// 必须先 SetTdlibParameters 打开客户端，再同步代理，
		// 否则 getProxies 在客户端未初始化时会卡满 60s catchTimeout，阻塞整个授权流程。
		w.mu.Lock()
		w.tdlibClient = c
		w.mu.Unlock()

		_, err := c.SetTdlibParameters(w.params)
		if err != nil {
			return err
		}

		if err := w.syncProxyLocked(c); err != nil {
			logger.Warnf("[Web] 启动时同步代理失败: %v", err)
		}

		// 客户端已初始化，尽早挂载更新监听以捕获连接状态
		if w.onClientReady != nil {
			w.onClientReady(c)
		}
		return nil

	case client.TypeAuthorizationStateWaitPhoneNumber:
		phone, ok := w.phone.Wait(w.done)
		if !ok {
			return fmt.Errorf("authorization cancelled")
		}
		logger.Infof("[Web] 授权状态: WaitPhoneNumber，提交手机号 -> %s", phone)
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
			logger.Warnf("[Web] 提交手机号失败: %v", err)
			w.setError(friendlyAuthError(err))
			return nil
		}
		w.setError("")
		return nil

	case client.TypeAuthorizationStateWaitCode:
		logger.Infof("[Web] 授权状态: WaitCode，验证码已发送，等待输入")
		code, ok := w.code.Wait(w.done)
		if !ok {
			return fmt.Errorf("authorization cancelled")
		}
		_, err := c.CheckAuthenticationCode(&client.CheckAuthenticationCodeRequest{
			Code: code,
		})
		if err != nil {
			logger.Warnf("[Web] 验证码校验失败: %v", err)
			w.setError(friendlyAuthError(err))
			return nil
		}
		w.setError("")
		return nil

	case client.TypeAuthorizationStateWaitPassword:
		logger.Infof("[Web] 授权状态: WaitPassword，等待两步验证密码")
		password, ok := w.password.Wait(w.done)
		if !ok {
			return fmt.Errorf("authorization cancelled")
		}
		_, err := c.CheckAuthenticationPassword(&client.CheckAuthenticationPasswordRequest{
			Password: password,
		})
		if err != nil {
			logger.Warnf("[Web] 两步验证密码校验失败: %v", err)
			w.setError(friendlyAuthError(err))
			return nil
		}
		w.setError("")
		return nil

	case client.TypeAuthorizationStateWaitOtherDeviceConfirmation:
		// 状态通过 CurrentState 暴露给前端展示确认链接
		return nil

	case client.TypeAuthorizationStateReady:
		w.setError("")
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
	w.setError("")
	w.phone.Set(phone)
}

func (w *WebAuthorizer) SetCode(code string) {
	w.setError("")
	w.code.Set(code)
}

func (w *WebAuthorizer) SetPassword(password string) {
	w.setError("")
	w.password.Set(password)
}

func (w *WebAuthorizer) setError(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastErr = msg
}

// friendlyAuthError 将 tdlib 认证错误映射为友好的中文提示。
// 无法识别时返回原始错误消息，保证前端总能获得错误原因。
func friendlyAuthError(err error) string {
	if rerr, ok := err.(client.ResponseError); ok && rerr.Err != nil {
		return mapAuthError(rerr.Err.Code, rerr.Err.Message)
	}
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "timeout") {
		return "连接超时，请检查网络或代理设置"
	}
	return msg
}

// mapAuthError 根据 tdlib 错误码/消息返回友好提示。
// 无法识别时返回原始错误消息；消息为空时返回错误码，保证返回值非空。
func mapAuthError(code int32, message string) string {
	upper := strings.ToUpper(message)
	switch {
	case strings.Contains(upper, "PHONE_CODE_INVALID"):
		return "验证码错误，请检查后重试"
	case strings.Contains(upper, "PHONE_CODE_EXPIRED"):
		return "验证码已过期，请重新获取"
	case strings.Contains(upper, "PHONE_NUMBER_INVALID"):
		return "手机号格式不正确，请检查后重试"
	case strings.Contains(upper, "PHONE_NUMBER_BANNED"):
		return "该手机号已被禁止使用"
	case strings.Contains(upper, "PHONE_NUMBER_FLOOD"), strings.Contains(upper, "FLOOD_WAIT"):
		return "操作过于频繁，请稍后再试"
	case strings.Contains(upper, "PHONE_NUMBER_OCCUPIED"):
		return "该手机号已被其他账号占用"
	case strings.Contains(upper, "PASSWORD_HASH_INVALID"):
		return "两步验证密码错误，请重试"
	case strings.Contains(upper, "PASSWORD_RECOVERY_NA"):
		return "该账号未设置两步验证密码恢复方式"
	case strings.Contains(upper, "SESSION_PASSWORD_NEEDED"):
		return "该账号需要两步验证密码"
	case strings.Contains(upper, "AUTH_KEY_UNREGISTERED"), strings.Contains(upper, "AUTH_KEY_INVALID"):
		return "登录会话已失效，请重新登录"
	case strings.Contains(upper, "CONNECTION"), strings.Contains(upper, "NETWORK"):
		return "无法连接到 Telegram，请检查网络或代理设置"
	case strings.Contains(upper, "PROXY"):
		return "代理连接失败，请检查代理设置"
	case strings.Contains(upper, "TIMEOUT"):
		return "连接超时，请检查网络或代理设置"
	}
	if message != "" {
		return message
	}
	return fmt.Sprintf("Telegram 错误码: %d", code)
}

func (w *WebAuthorizer) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}

// Client 返回 tdlib 客户端（登录流程开始后可用），未初始化时为 nil。
func (w *WebAuthorizer) Client() *client.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tdlibClient
}

// ProxyConfig 返回当前代理配置副本。
func (w *WebAuthorizer) ProxyConfig() config.Sock5Proxy {
	w.proxyMu.Lock()
	defer w.proxyMu.Unlock()
	return w.proxyCfg
}

// SetProxyConfig 更新代理配置。下一次 Handle 同步或 SyncProxyNow 时生效。
func (w *WebAuthorizer) SetProxyConfig(cfg config.Sock5Proxy) {
	w.proxyMu.Lock()
	w.proxyCfg = cfg
	w.proxyMu.Unlock()
}

// SyncProxyNow 使用当前 tdlib 客户端同步代理（清旧 + 按最新配置重建）。
// 客户端尚未就绪时返回 nil。
func (w *WebAuthorizer) SyncProxyNow() error {
	c := w.Client()
	if c == nil {
		return nil
	}
	return w.syncProxyLocked(c)
}

// syncProxyLocked 删除 tdlib 全部代理并按最新配置重建。
func (w *WebAuthorizer) syncProxyLocked(c *client.Client) error {
	w.proxyMu.Lock()
	cfg := w.proxyCfg
	w.proxyMu.Unlock()
	return teleapp.SyncProxies(c, &cfg)
}
