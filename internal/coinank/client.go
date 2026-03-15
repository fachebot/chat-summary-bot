// Package coinank 提供 Coinank API 客户端
package coinank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/enetx/g"
	"github.com/enetx/surf"
	"github.com/fachebot/talk-trace-bot/internal/config"
	"github.com/shopspring/decimal"
)

// Client 是 Coinank API 客户端
type Client struct {
	proxy      config.Sock5Proxy
	httpClient *http.Client
}

// NewClient 创建新的 Coinank API 客户端
func NewClient(proxy config.Sock5Proxy) (*Client, error) {
	builder := surf.NewClient().Builder()
	if proxy.Enable {
		builder = builder.Proxy(g.String(fmt.Sprintf("socks5://%s:%d", proxy.Host, proxy.Port)))
	}
	httpClient := builder.
		Impersonate().
		Chrome().
		Build().
		Unwrap().
		Std()

	return &Client{
		proxy:      proxy,
		httpClient: httpClient,
	}, nil
}

const baseURL = "https://api.coinank.com"

func (c *Client) handleResponse(resp *Response) error {
	if !resp.Success {
		return fmt.Errorf("coinank api error: %s", resp.Message)
	}
	return nil
}

// GetMvrvZscore 获取 MVRV Zscore 数据
// MVRV Zscore 衡量比特币市值与实现值的比率
func (c *Client) GetMvrvZscore(ctx context.Context) (BMvrvZscoreList, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/indicatorapi/chain/index/charts", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("coinank-apikey", "LWIzMWUtYzU0Ny1kMjk5LWI2ZDA3Yjc2MzFhYmIyZDkwM2RkfDM5OTU3MjU0MTIzMzEzNDc=")

	q := req.URL.Query()
	q.Add("type", "/charts/mvrv-zscore/")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result MvrvZscoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if err := c.handleResponse(&result.Response); err != nil {
		return nil, err
	}

	if len(result.Data.Line.TimeList) != len(result.Data.Line.Value4) {
		return nil, errors.New("time and zscore data length mismatch")
	}

	ret := make(BMvrvZscoreList, 0, len(result.Data.Line.TimeList))
	for idx, unixMs := range result.Data.Line.TimeList {
		zscore := decimal.Zero
		if result.Data.Line.Value4[idx] != nil {
			zscore = *result.Data.Line.Value4[idx]
		}

		ret = append(ret, MvrvZscore{
			Time:   time.UnixMilli(unixMs),
			Zscore: zscore,
		})
	}

	return ret, nil
}

// GetBitcoinCVDD 获取比特币 CVDD (累计价值天数销毁) 数据
// CVDD 用于识别市场周期
func (c *Client) GetBitcoinCVDD(ctx context.Context) (BitcoinCVDDList, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/indicatorapi/chain/index/charts", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("coinank-apikey", "LWIzMWUtYzU0Ny1kMjk5LWI2ZDA3Yjc2MzFhYmIyZDkwM2RkfDM5OTU3MjQ0NTAwMzAzNDc=")

	q := req.URL.Query()
	q.Add("type", "/charts/bitcoin-price-prediction/")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result PricePredictionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if err := c.handleResponse(&result.Response); err != nil {
		return nil, err
	}

	if len(result.Data.Line.TimeList) != len(result.Data.Line.Value1) {
		return nil, errors.New("time and cvdd data length mismatch")
	}

	ret := make(BitcoinCVDDList, 0, len(result.Data.Line.TimeList))
	if result.Data.Line.Value1 != nil && result.Data.Line.BtcPrice != nil {
		for idx, unixMs := range result.Data.Line.TimeList {
			cvdd := decimal.Zero
			if result.Data.Line.Value1[idx] != nil {
				cvdd = *result.Data.Line.Value1[idx]
			}

			price := decimal.Zero
			if result.Data.Line.BtcPrice[idx] != nil {
				price = *result.Data.Line.BtcPrice[idx]
			}

			ret = append(ret, BitcoinCVDD{
				Time:  time.UnixMilli(unixMs),
				CVDD:  cvdd,
				Price: price,
			})
		}
	}

	return ret, nil
}
