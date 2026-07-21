package web

import (
	"fmt"
	"sync"

	"github.com/zelenin/go-tdlib/client"
)

type WebAuthorizer struct {
	mu          sync.Mutex
	tdlibClient *client.Client
	params      *client.SetTdlibParametersRequest
	state       client.AuthorizationState
	phoneNumber chan string
	code        chan string
	password    chan string
	done        chan struct{}
}

func NewWebAuthorizer(params *client.SetTdlibParametersRequest) *WebAuthorizer {
	return &WebAuthorizer{
		params:      params,
		phoneNumber: make(chan string, 1),
		code:        make(chan string, 1),
		password:    make(chan string, 1),
		done:        make(chan struct{}),
	}
}

func (w *WebAuthorizer) Handle(c *client.Client, state client.AuthorizationState) error {
	w.mu.Lock()
	w.tdlibClient = c
	w.state = state
	w.mu.Unlock()

	switch state.AuthorizationStateType() {
	case client.TypeAuthorizationStateWaitTdlibParameters:
		_, err := c.SetTdlibParameters(w.params)
		return err

	case client.TypeAuthorizationStateWaitPhoneNumber:
		select {
		case phone := <-w.phoneNumber:
			_, err := c.SetAuthenticationPhoneNumber(&client.SetAuthenticationPhoneNumberRequest{
				PhoneNumber: phone,
				Settings: &client.PhoneNumberAuthenticationSettings{
					AllowFlashCall:       false,
					IsCurrentPhoneNumber: false,
					AllowSmsRetrieverApi: false,
				},
			})
			return err
		case <-w.done:
			return fmt.Errorf("authorization cancelled")
		}

	case client.TypeAuthorizationStateWaitCode:
		select {
		case code := <-w.code:
			_, err := c.CheckAuthenticationCode(&client.CheckAuthenticationCodeRequest{
				Code: code,
			})
			return err
		case <-w.done:
			return fmt.Errorf("authorization cancelled")
		}

	case client.TypeAuthorizationStateWaitPassword:
		select {
		case password := <-w.password:
			_, err := c.CheckAuthenticationPassword(&client.CheckAuthenticationPasswordRequest{
				Password: password,
			})
			return err
		case <-w.done:
			return fmt.Errorf("authorization cancelled")
		}

	case client.TypeAuthorizationStateReady:
		close(w.done)
		return nil

	case client.TypeAuthorizationStateClosed:
		return nil

	default:
		return client.NotSupportedAuthorizationState(state)
	}
}

func (w *WebAuthorizer) Close() {
	close(w.phoneNumber)
	close(w.code)
	close(w.password)
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
	w.phoneNumber <- phone
}

func (w *WebAuthorizer) SetCode(code string) {
	w.code <- code
}

func (w *WebAuthorizer) SetPassword(password string) {
	w.password <- password
}
