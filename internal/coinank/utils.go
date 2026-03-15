package coinank

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type CVDDSortItem struct {
	Time      time.Time
	Price     decimal.Decimal
	CVDD      decimal.Decimal
	Deviation decimal.Decimal
}

type CVDDSortList []CVDDSortItem

func (l CVDDSortList) Len() int           { return len(l) }
func (l CVDDSortList) Swap(i, j int)      { l[i], l[j] = l[j], l[i] }
func (l CVDDSortList) Less(i, j int) bool { return l[i].Deviation.LessThan(l[j].Deviation) }

// CalculateCVDDDeviation 计算 CVDD 与价格的偏差百分比并排序
func CalculateCVDDDeviation(list BitcoinCVDDList) CVDDSortList {
	result := make(CVDDSortList, 0, len(list))
	for _, item := range list {
		if item.Price.IsZero() || item.CVDD.IsZero() {
			continue
		}

		deviation := item.Price.Sub(item.CVDD).Div(item.Price)
		result = append(result, CVDDSortItem{
			Time:      item.Time,
			Price:     item.Price,
			CVDD:      item.CVDD,
			Deviation: deviation,
		})
	}
	sort.Sort(result)
	return result
}
