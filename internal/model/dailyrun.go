package model

import (
	"context"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/ent"
	"github.com/fachebot/chat-summary-bot/internal/ent/dailyrun"
)

type DailyRunModel struct {
	client *ent.DailyRunClient
}

func NewDailyRunModel(client *ent.DailyRunClient) *DailyRunModel {
	return &DailyRunModel{client: client}
}

// Create 创建 DailyRun 记录
func (m *DailyRunModel) Create(ctx context.Context, startTime, endTime time.Time, status dailyrun.Status) (*ent.DailyRun, error) {
	return m.client.Create().
		SetStartTime(startTime).
		SetEndTime(endTime).
		SetStatus(status).
		Save(ctx)
}

// GetOrCreate 获取或创建 DailyRun（用于 runDailySummary 开始时）
// 若已存在相同 start_time/end_time 的记录则返回现有记录
func (m *DailyRunModel) GetOrCreate(ctx context.Context, startTime, endTime time.Time, status dailyrun.Status) (*ent.DailyRun, error) {
	existing, err := m.client.Query().
		Where(
			dailyrun.StartTimeEQ(startTime),
			dailyrun.EndTimeEQ(endTime),
		).
		First(ctx)

	if err == nil {
		return existing, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	return m.Create(ctx, startTime, endTime, status)
}

// GetByDateRange 查询指定日期区间的 DailyRun 记录
func (m *DailyRunModel) GetByDateRange(ctx context.Context, startTime, endTime time.Time) (*ent.DailyRun, error) {
	return m.client.Query().
		Where(
			dailyrun.StartTimeEQ(startTime),
			dailyrun.EndTimeEQ(endTime),
		).
		First(ctx)
}

// GetIncompleteRuns 查询所有未完成的 DailyRun（pending 或 in_progress）
func (m *DailyRunModel) GetIncompleteRuns(ctx context.Context) ([]*ent.DailyRun, error) {
	return m.client.Query().
		Where(
			dailyrun.Or(
				dailyrun.StatusEQ(dailyrun.StatusPending),
				dailyrun.StatusEQ(dailyrun.StatusInProgress),
			),
		).
		Order(dailyrun.ByCreateTime()).
		All(ctx)
}

// MarkCompleted 标记 DailyRun 完成
func (m *DailyRunModel) MarkCompleted(ctx context.Context, id int) error {
	return m.client.UpdateOneID(id).SetStatus(dailyrun.StatusCompleted).Exec(ctx)
}

// MarkFailed 标记 DailyRun 失败
func (m *DailyRunModel) MarkFailed(ctx context.Context, id int, errorMsg string) error {
	return m.client.UpdateOneID(id).
		SetStatus(dailyrun.StatusFailed).
		SetErrorMessage(errorMsg).
		Exec(ctx)
}
