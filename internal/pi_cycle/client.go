package pi_cycle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/enetx/g"
	"github.com/enetx/surf"
	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/shopspring/decimal"
)

const (
	binanceBaseURL = "https://api.binance.com"

	longSMAPeriod   = 471
	shortEMAPeriod  = 150
	longMAMultiple  = 0.745
	shortMAMultiple = 1.0

	// 为了更接近 TradingView，给 EMA/SMA 足够 warmup 历史
	defaultWarmupBars = 1000
)

type Client struct {
	proxy      config.Sock5Proxy
	httpClient *http.Client
}

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

// GetKlines 获取 K 线数据
// symbol: 如 BTCUSDT
// interval: 如 1d / 4h
// limit: 0 表示尽可能多拉取；>0 表示只返回最近 limit 根
func (c *Client) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	fetchLimit := 1000

	var allKlines []Kline
	var endTime int64

	for {
		url := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=%s&limit=%d", binanceBaseURL, symbol, interval, fetchLimit)
		if endTime > 0 {
			url += fmt.Sprintf("&endTime=%d", endTime)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request failed: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		var rawData [][]decimal.Decimal
		func() {
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
				return
			}

			if decodeErr := json.NewDecoder(resp.Body).Decode(&rawData); decodeErr != nil {
				err = fmt.Errorf("decode response failed: %w", decodeErr)
				return
			}
		}()
		if err != nil {
			return nil, err
		}

		if len(rawData) == 0 {
			break
		}

		pageKlines := make([]Kline, 0, len(rawData))
		for _, item := range rawData {
			openTime := item[0].IntPart()
			pageKlines = append(pageKlines, Kline{
				OpenTime: time.UnixMilli(openTime),
				Open:     item[1],
				High:     item[2],
				Low:      item[3],
				Close:    item[4],
				Volume:   item[5],
			})
		}

		// 因为当前页是“更早的数据”，并且页内本身已经是升序，
		// 所以应该插到 allKlines 前面，保持整体仍然升序。
		allKlines = append(pageKlines, allKlines...)

		if len(rawData) < fetchLimit {
			break
		}

		// 下一页继续向更早时间翻
		endTime = rawData[0][0].IntPart() - 1

		// 如果指定了 limit，只保留最近的 limit 根即可
		if limit > 0 && len(allKlines) >= limit {
			if len(allKlines) > limit {
				allKlines = allKlines[len(allKlines)-limit:]
			}
			break
		}
	}

	allKlines = dedupeKlines(allKlines)

	return allKlines, nil
}

func dedupeKlines(klines []Kline) []Kline {
	if len(klines) <= 1 {
		return klines
	}

	result := make([]Kline, 0, len(klines))
	result = append(result, klines[0])

	for i := 1; i < len(klines); i++ {
		if !klines[i].OpenTime.Equal(klines[i-1].OpenTime) {
			result = append(result, klines[i])
		}
	}

	return result
}

// CalculateSMAWithNA 使用 nil 模拟 Pine 的 na
func CalculateSMAWithNA(prices []decimal.Decimal, period int) []*decimal.Decimal {
	result := make([]*decimal.Decimal, len(prices))
	if period <= 0 || len(prices) < period {
		return result
	}

	sum := decimal.Zero
	periodDec := decimal.NewFromInt(int64(period))

	for i := 0; i < len(prices); i++ {
		sum = sum.Add(prices[i])

		if i >= period {
			sum = sum.Sub(prices[i-period])
		}

		if i >= period-1 {
			v := sum.Div(periodDec)
			result[i] = &v
		}
	}

	return result
}

// CalculateEMAWithNA 更贴近 ta.ema 的经典做法：
// 1. 前 period-1 为 na
// 2. 第 period 根使用这 period 根 SMA 作为初始 EMA
// 3. 之后递推
func CalculateEMAWithNA(prices []decimal.Decimal, period int) []*decimal.Decimal {
	result := make([]*decimal.Decimal, len(prices))
	if period <= 0 || len(prices) < period {
		return result
	}

	multiplier := decimal.NewFromInt(2).Div(decimal.NewFromInt(int64(period + 1)))

	sum := decimal.Zero
	for i := 0; i < period; i++ {
		sum = sum.Add(prices[i])
	}

	initial := sum.Div(decimal.NewFromInt(int64(period)))
	result[period-1] = &initial

	prev := initial
	for i := period; i < len(prices); i++ {
		ema := prices[i].Sub(prev).Mul(multiplier).Add(prev)
		result[i] = &ema
		prev = ema
	}

	return result
}

func MultiplySeries(values []*decimal.Decimal, multiplier decimal.Decimal) []*decimal.Decimal {
	result := make([]*decimal.Decimal, len(values))
	for i, v := range values {
		if v == nil {
			continue
		}
		val := v.Mul(multiplier)
		result[i] = &val
	}
	return result
}

func SubSeries(a, b []*decimal.Decimal) []*decimal.Decimal {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	result := make([]*decimal.Decimal, n)
	for i := 0; i < n; i++ {
		if a[i] == nil || b[i] == nil {
			continue
		}
		val := a[i].Sub(*b[i])
		result[i] = &val
	}
	return result
}

// Pine 等价：ta.crossunder(a, b) <=> a[1] >= b[1] and a < b
func crossUnder(prevA, prevB, curA, curB *decimal.Decimal) bool {
	if prevA == nil || prevB == nil || curA == nil || curB == nil {
		return false
	}
	return prevA.GreaterThanOrEqual(*prevB) && curA.LessThan(*curB)
}

// Pine 等价：ta.crossover(a, b) <=> a[1] <= b[1] and a > b
func crossOver(prevA, prevB, curA, curB *decimal.Decimal) bool {
	if prevA == nil || prevB == nil || curA == nil || curB == nil {
		return false
	}
	return prevA.LessThanOrEqual(*prevB) && curA.GreaterThan(*curB)
}

// CalculatePiCycle 计算 Pi Cycle Bottom
// symbol 默认 BTCUSDT
// interval 默认 1d
// visibleLimit: 最终返回最近多少根；0 表示返回所有
//
// 内部会自动增加 warmup 历史，以减小 EMA 初值误差。
func (c *Client) CalculatePiCycle(ctx context.Context, symbol, interval string, visibleLimit int) (*PiCycleResult, error) {
	if symbol == "" {
		symbol = "BTCUSDT"
	}
	if interval == "" {
		interval = "1d"
	}

	// 为了更贴近 TV，内部多拉 warmup 数据
	internalLimit := 0
	if visibleLimit > 0 {
		internalLimit = visibleLimit + defaultWarmupBars
	}

	klines, err := c.GetKlines(ctx, symbol, interval, internalLimit)
	if err != nil {
		return nil, err
	}

	if len(klines) < longSMAPeriod {
		return nil, fmt.Errorf("insufficient kline data: need at least %d, got %d", longSMAPeriod, len(klines))
	}

	prices := make([]decimal.Decimal, len(klines))
	for i, k := range klines {
		prices[i] = k.Close
	}

	sma471 := CalculateSMAWithNA(prices, longSMAPeriod)
	ema150 := CalculateEMAWithNA(prices, shortEMAPeriod)

	longMA := MultiplySeries(sma471, decimal.NewFromFloat(longMAMultiple))
	shortMA := MultiplySeries(ema150, decimal.NewFromFloat(shortMAMultiple))
	diff := SubSeries(shortMA, longMA)

	signals := detectCrossSignals(klines, longMA, shortMA, diff)

	result := &PiCycleResult{
		Klines:       klines,
		LongMA:       longMA,
		ShortMA:      shortMA,
		Diff:         diff,
		Signals:      signals,
		WarmupUsed:   defaultWarmupBars,
		SourceSymbol: symbol,
		Interval:     interval,
	}

	// 最终只裁剪显示窗口，但信号保留窗口内的
	if visibleLimit > 0 && len(result.Klines) > visibleLimit {
		cut := len(result.Klines) - visibleLimit

		startTime := result.Klines[cut].OpenTime

		result.Klines = result.Klines[cut:]
		result.LongMA = result.LongMA[cut:]
		result.ShortMA = result.ShortMA[cut:]
		result.Diff = result.Diff[cut:]

		filteredSignals := make([]PiCycleSignal, 0, len(result.Signals))
		for _, s := range result.Signals {
			if !s.Time.Before(startTime) {
				filteredSignals = append(filteredSignals, s)
			}
		}
		result.Signals = filteredSignals
	}

	return result, nil
}

func detectCrossSignals(
	klines []Kline,
	longMA []*decimal.Decimal,
	shortMA []*decimal.Decimal,
	diff []*decimal.Decimal,
) []PiCycleSignal {
	signals := make([]PiCycleSignal, 0)

	start := longSMAPeriod - 1
	if shortEMAPeriod-1 > start {
		start = shortEMAPeriod - 1
	}
	if start < 1 {
		start = 1
	}

	for i := start; i < len(klines); i++ {
		if crossUnder(shortMA[i-1], longMA[i-1], shortMA[i], longMA[i]) {
			price := klines[i].Close.Mul(decimal.NewFromFloat(1.1)) // 对齐 Pine 的 plotshape y 值
			d := decimal.Zero
			if diff[i] != nil {
				d = *diff[i]
			}

			signals = append(signals, PiCycleSignal{
				Time:       klines[i].OpenTime,
				Price:      price,
				Close:      klines[i].Close,
				LongMA:     *longMA[i],
				ShortMA:    *shortMA[i],
				Diff:       d,
				SignalType: SignalTypeCrossUnder,
			})
		}

		if crossOver(shortMA[i-1], longMA[i-1], shortMA[i], longMA[i]) {
			d := decimal.Zero
			if diff[i] != nil {
				d = *diff[i]
			}

			signals = append(signals, PiCycleSignal{
				Time:       klines[i].OpenTime,
				Price:      klines[i].Close,
				Close:      klines[i].Close,
				LongMA:     *longMA[i],
				ShortMA:    *shortMA[i],
				Diff:       d,
				SignalType: SignalTypeCrossOver,
			})
		}
	}

	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Time.After(signals[j].Time)
	})

	return signals
}

// GetLatestCrossUnderSignal 获取最近一次 Pi Cycle Bottom 信号
func (c *Client) GetLatestCrossUnderSignal(ctx context.Context) (*PiCycleSignal, error) {
	result, err := c.CalculatePiCycle(ctx, "BTCUSDT", "1d", 0)
	if err != nil {
		return nil, err
	}

	for _, signal := range result.Signals {
		if signal.SignalType == SignalTypeCrossUnder {
			return &signal, nil
		}
	}
	return nil, nil
}
