// Package market_indicators 提供BTC抄底指标的缓存服务
// 每隔一小时自动更新 PiCycle、MvrvZscore、BitcoinCVDD 三类指标数据
package market_indicators

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fachebot/talk-trace-bot/internal/coinank"
	"github.com/fachebot/talk-trace-bot/internal/logger"
	"github.com/fachebot/talk-trace-bot/internal/pi_cycle"
	"github.com/fachebot/talk-trace-bot/internal/svc"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

const (
	// UpdateInterval 定时更新间隔
	UpdateInterval = time.Hour
)

// MarketIndicators BTC抄底指标
type MarketIndicators struct {
	svcCtx      *svc.ServiceContext
	mu          sync.RWMutex
	lastUpdate  time.Time
	bitcoinCVDD *coinank.BitcoinCVDD    // Bitcoin CVDD 最新值
	mvrvZscore  *coinank.MvrvZscore     // MVRV Z-Score 最新值
	piCycle     *pi_cycle.PiCycleResult // PiCycle Bottom 最新值
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// New 创建 MarketIndicators 实例
func New(svcCtx *svc.ServiceContext) *MarketIndicators {
	ctx, cancel := context.WithCancel(context.Background())
	return &MarketIndicators{
		svcCtx: svcCtx,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start 启动后台定时更新任务
// 启动时立即执行一次更新，成功后每1小时更新一次，失败则3秒后重试
func (m *MarketIndicators) Start() {
	logger.Infof("[MarketIndicators] 启动市场指标缓存服务")

	m.wg.Go(func() {
		// 使用0值ticker立即触发第一次更新
		ticker := time.NewTicker(1)
		defer ticker.Stop()

		for {
			select {
			case <-m.ctx.Done():
				logger.Infof("[MarketIndicators] 缓存服务已停止")
				return
			case <-ticker.C:
				if m.updateOnce() {
					// 更新成功后，重置为1小时间隔
					ticker.Reset(UpdateInterval)
				} else {
					// 更新失败后，3秒后重试
					logger.Infof("[MarketIndicators] 更新失败，重置定时器")
					ticker.Reset(time.Second * 3)
				}
			}
		}
	})
}

// Stop 停止后台更新任务，会等待所有任务完成后返回
func (m *MarketIndicators) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

// updateOnce 执行一次指标更新
// 返回 true 表示全部更新成功，false 表示有错误发生
func (m *MarketIndicators) updateOnce() bool {
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()

	logger.Infof("[MarketIndicators] 开始更新市场指标...")

	var piCycle *pi_cycle.PiCycleResult
	var mvrv *coinank.MvrvZscore
	var cvdd *coinank.BitcoinCVDD
	var errs []string

	// 获取 PiCycle Bottom 信号
	piResult, err := m.svcCtx.PiCycleClient.CalculatePiCycle(ctx, "BTCUSDT", "1d", 0)
	if err != nil {
		errs = append(errs, fmt.Sprintf("PiCycle: %v", err))
		logger.Errorf("[MarketIndicators] 获取PiCycle数据失败: %v", err)
	} else {
		piCycle = piResult
	}

	// 获取 MVRV Z-Score
	mvrvList, err := m.svcCtx.CoinankClient.GetMvrvZscore(ctx)
	if err != nil {
		errs = append(errs, fmt.Sprintf("MVRV Zscore: %v", err))
		logger.Errorf("[MarketIndicators] 获取MVRV Zscore失败: %v", err)
	} else if len(mvrvList) > 0 {
		mvrv = &mvrvList[len(mvrvList)-1]
	}

	// 获取 Bitcoin CVDD
	cvddList, err := m.svcCtx.CoinankClient.GetBitcoinCVDD(ctx)
	if err != nil {
		errs = append(errs, fmt.Sprintf("CVDD: %v", err))
		logger.Errorf("[MarketIndicators] 获取Bitcoin CVDD失败: %v", err)
	} else if len(cvddList) > 0 {
		cvdd = &cvddList[len(cvddList)-1]
	}

	// 更新缓存
	m.mu.Lock()
	m.lastUpdate = time.Now()
	m.piCycle = piCycle
	m.mvrvZscore = mvrv
	m.bitcoinCVDD = cvdd
	m.mu.Unlock()

	if len(errs) > 0 {
		logger.Warnf("[MarketIndicators] 部分指标更新失败: %s", strings.Join(errs, "; "))
		return false
	}

	logger.Infof("[MarketIndicators] 市场指标更新成功")
	return true
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 UTC")
}

// GetFormattedText 获取格式化的指标文本，用于发送给用户
func (m *MarketIndicators) GetFormattedText() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sb strings.Builder

	sb.WriteString("<b>📊 BTC抄底指标</b>\n\n")

	if m.bitcoinCVDD != nil {
		sb.WriteString(fmt.Sprintf("📉 <a href=\"%s\">Bitcoin CVDD</a>\n", "https://coinank.com/chart/indicator/btc-price-prediction"))
		sb.WriteString(fmt.Sprintf("  CVDD: <code>%s</code>\n", m.bitcoinCVDD.CVDD.Truncate(2)))
		sb.WriteString(fmt.Sprintf("  BTC价格: <code>%s</code>\n", m.bitcoinCVDD.Price.Truncate(2)))
		deviation := m.bitcoinCVDD.Price.Sub(m.bitcoinCVDD.CVDD).Div(m.bitcoinCVDD.Price)
		sb.WriteString(fmt.Sprintf("  偏差百分比: <code>%s%%</code>\n", deviation.Mul(decimal.NewFromInt(100)).Truncate(2)))
		sb.WriteString(fmt.Sprintf("  抄底条件: <code>&lt15%%</code> %s\n", lo.If(deviation.LessThan(decimal.NewFromFloat(0.15)), "✅").Else("❌")))
		sb.WriteString(fmt.Sprintf("  更新时间: <code>%s</code>\n\n", formatTime(m.bitcoinCVDD.Time)))
	} else {
		sb.WriteString(fmt.Sprintf("📉 <a href=\"%s\">Bitcoin CVDD</a>\n暂无数据\n\n", "https://coinank.com/chart/indicator/btc-price-prediction"))
	}

	if m.mvrvZscore != nil {
		sb.WriteString(fmt.Sprintf("📈 <a href=\"%s\">MVRV Z-Score</a>\n", "https://coinank.com/chart/indicator/mvrv-z-score"))
		sb.WriteString(fmt.Sprintf("  当前值: <code>%s</code>\n", m.mvrvZscore.Zscore.Truncate(2)))
		sb.WriteString(fmt.Sprintf("  抄底条件: <code>&lt0</code> %s\n", lo.If(m.mvrvZscore.Zscore.LessThan(decimal.Zero), "✅").Else("❌")))
		sb.WriteString(fmt.Sprintf("  更新时间: <code>%s</code>\n\n", formatTime(m.mvrvZscore.Time)))
	} else {
		sb.WriteString(fmt.Sprintf("📈 <a href=\"%s\">MVRV Z-Score</a>\n暂无数据\n\n", "https://coinank.com/chart/indicator/mvrv-z-score"))
	}

	if m.piCycle != nil && len(m.piCycle.Klines) > 0 && len(m.piCycle.LongMA) > 0 && len(m.piCycle.ShortMA) > 0 {
		sb.WriteString(fmt.Sprintf("🔄 <a href=\"%s\">Pi Cycle Bottom</a>\n", "https://www.tradingview.com/script/IFf6VobP-Pi-Cycle-Bottom-Indicator"))

		kline := m.piCycle.Klines[len(m.piCycle.Klines)-1]
		longMA := m.piCycle.LongMA[len(m.piCycle.LongMA)-1]
		shortMA := m.piCycle.ShortMA[len(m.piCycle.ShortMA)-1]

		var signal *pi_cycle.PiCycleSignal
		if len(m.piCycle.Signals) > 0 {
			signal = &m.piCycle.Signals[0]
		}

		sb.WriteString(fmt.Sprintf("  价格: <code>%s</code>\n", kline.Close.Truncate(2)))
		sb.WriteString(fmt.Sprintf("  Long MA: <code>%s</code>\n", longMA.Truncate(2)))
		sb.WriteString(fmt.Sprintf("  Short MA: <code>%s</code>\n", shortMA.Truncate(2)))
		if signal != nil {
			sb.WriteString(fmt.Sprintf("  上次信号: <code>%s</code>\n", lo.If(signal.SignalType == pi_cycle.SignalTypeCrossUnder, "死叉").Else("金叉")))
			sb.WriteString(fmt.Sprintf("  触发价格: <code>%s</code>\n", signal.Price.Truncate(2)))
			sb.WriteString(fmt.Sprintf("  触发时间: <code>%s</code>\n", formatTime(signal.Time)))
			sb.WriteString(fmt.Sprintf("  抄底条件: <code>死叉信号</code> %s\n", lo.If(signal.SignalType == pi_cycle.SignalTypeCrossUnder, "✅").Else("❌")))
		}

		sb.WriteString(fmt.Sprintf("  更新时间: <code>%s</code>\n\n", formatTime(kline.OpenTime)))
	} else {
		sb.WriteString(fmt.Sprintf("🔄 <a href=\"%s\">Pi Cycle Bottom</a>\n暂无信号\n\n", "https://www.tradingview.com/script/IFf6VobP-Pi-Cycle-Bottom-Indicator"))
	}

	return sb.String()
}

// GetLastUpdateTime 获取最后更新时间
func (m *MarketIndicators) GetLastUpdateTime() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastUpdate
}
