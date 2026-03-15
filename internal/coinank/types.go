package coinank

import (
	"time"

	"github.com/shopspring/decimal"
)

type Response struct {
	Code    int    `json:"code"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type Line struct {
	TimeList []int64            `json:"timeList"`
	BtcPrice []*decimal.Decimal `json:"btcPrice"`
	Value1   []*decimal.Decimal `json:"value1,omitempty"`
	Value2   []*decimal.Decimal `json:"value2,omitempty"`
	Value3   []*decimal.Decimal `json:"value3,omitempty"`
	Value4   []*decimal.Decimal `json:"value4,omitempty"`
	Value5   []*decimal.Decimal `json:"value5,omitempty"`
	Value6   []*decimal.Decimal `json:"value6,omitempty"`
	Value7   []*decimal.Decimal `json:"value7,omitempty"`
	Value8   []*decimal.Decimal `json:"value8,omitempty"`
	Value9   []*decimal.Decimal `json:"value9,omitempty"`
	Value10  []*decimal.Decimal `json:"value10,omitempty"`
	Value11  []*decimal.Decimal `json:"value11,omitempty"`
	Value12  []*decimal.Decimal `json:"value12,omitempty"`
}

type Charts struct {
	Line Line `json:"line"`
}

type MvrvZscoreResponse struct {
	Response
	Data Charts `json:"data"`
}

type MvrvZscore struct {
	Time   time.Time
	Zscore decimal.Decimal
}

type BMvrvZscoreList []MvrvZscore

type PricePredictionResponse struct {
	Response
	Data Charts `json:"data"`
}

type BitcoinCVDD struct {
	Time  time.Time
	Price decimal.Decimal
	CVDD  decimal.Decimal
}

type BitcoinCVDDList []BitcoinCVDD
