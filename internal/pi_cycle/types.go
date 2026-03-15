package pi_cycle

import (
	"time"

	"github.com/shopspring/decimal"
)

// Kline K线数据
type Kline struct {
	OpenTime time.Time
	Open     decimal.Decimal
	High     decimal.Decimal
	Low      decimal.Decimal
	Close    decimal.Decimal
	Volume   decimal.Decimal
}

const (
	SignalTypeNone       = ""
	SignalTypeCrossUnder = "CROSSUNDER" // Pi Cycle Bottom 触发
	SignalTypeCrossOver  = "CROSSOVER"  // 反向上穿
)

// PiCycleSignal 信号
type PiCycleSignal struct {
	Time       time.Time
	Price      decimal.Decimal // 对应 Pine plotshape 的 y 值；bottom 用 close * 1.1
	Close      decimal.Decimal // 原始收盘价
	LongMA     decimal.Decimal
	ShortMA    decimal.Decimal
	Diff       decimal.Decimal
	SignalType string
}

// PiCycleResult 计算结果
type PiCycleResult struct {
	Klines       []Kline
	LongMA       []*decimal.Decimal
	ShortMA      []*decimal.Decimal
	Diff         []*decimal.Decimal
	Signals      []PiCycleSignal
	WarmupUsed   int
	SourceSymbol string
	Interval     string
}
