package teleapp

import (
	"fmt"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"

	"github.com/zelenin/go-tdlib/client"
)

// SyncProxies 删除 tdlib 中所有代理，然后按配置重新添加（启用）。
// 在每次程序启动或代理配置变更时调用，避免残留旧代理导致无法选择正确的代理。
func SyncProxies(c *client.Client, cfg *config.Sock5Proxy) error {
	if c == nil {
		return nil
	}

	proxies, err := c.GetProxies()
	if err != nil {
		return fmt.Errorf("获取代理列表失败: %w", err)
	}
	for _, p := range proxies.Proxies {
		if _, err := c.RemoveProxy(&client.RemoveProxyRequest{ProxyId: p.Id}); err != nil {
			logger.Warnf("[Proxy] 移除代理失败 id=%d: %v", p.Id, err)
		}
	}

	if cfg != nil && cfg.Enable {
		if _, err := c.AddProxy(buildAddProxyRequest(cfg)); err != nil {
			return fmt.Errorf("添加代理失败: %w", err)
		}
	}
	return nil
}

// ProxyOption 返回一个 client.Option，在客户端创建时同步代理（清旧 + 按配置重建）。
func ProxyOption(cfg *config.Sock5Proxy) client.Option {
	return func(c *client.Client) {
		if err := SyncProxies(c, cfg); err != nil {
			logger.Warnf("[Proxy] 启动时同步代理失败: %v", err)
		}
	}
}

// buildAddProxyRequest 根据配置构造 tdlib 的 AddProxy 请求。
func buildAddProxyRequest(cfg *config.Sock5Proxy) *client.AddProxyRequest {
	req := &client.AddProxyRequest{
		Server: cfg.Host,
		Port:   cfg.Port,
		Enable: true,
	}
	switch cfg.ProxyType() {
	case "http":
		req.Type = &client.ProxyTypeHttp{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	case "mtproto":
		req.Type = &client.ProxyTypeMtproto{
			Secret: cfg.Secret,
		}
	default:
		req.Type = &client.ProxyTypeSocks5{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}
	return req
}
