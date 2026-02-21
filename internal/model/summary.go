package model

import (
	"context"
	"time"

	"github.com/fachebot/talk-trace-bot/internal/ent"
	"github.com/fachebot/talk-trace-bot/internal/ent/summary"
)

type SummaryModel struct {
	client *ent.SummaryClient
}

func NewSummaryModel(client *ent.SummaryClient) *SummaryModel {
	return &SummaryModel{client: client}
}

type SummaryData struct {
	ChatID         int64
	SenderID       int64
	SenderName     string
	SenderUsername *string
	SenderNickname *string
	SummaryDate    time.Time
	Content        string
}

// Create 创建摘要
func (m *SummaryModel) Create(ctx context.Context, data *SummaryData) (*ent.Summary, error) {
	create := m.client.Create().
		SetChatID(data.ChatID).
		SetSenderID(data.SenderID).
		SetSenderName(data.SenderName).
		SetSummaryDate(data.SummaryDate).
		SetContent(data.Content)

	if data.SenderUsername != nil {
		create.SetSenderUsername(*data.SenderUsername)
	}
	if data.SenderNickname != nil {
		create.SetSenderNickname(*data.SenderNickname)
	}

	return create.Save(ctx)
}

// getByChatSenderAndDate 按群组、发送者、摘要日期（同一天）查询一条摘要（统一使用 UTC）
func (m *SummaryModel) getByChatSenderAndDate(ctx context.Context, chatID, senderID int64, summaryDate time.Time) (*ent.Summary, error) {
	startOfDay := time.Date(summaryDate.Year(), summaryDate.Month(), summaryDate.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)
	return m.client.Query().
		Where(
			summary.ChatIDEQ(chatID),
			summary.SenderIDEQ(senderID),
			summary.SummaryDateGTE(startOfDay),
			summary.SummaryDateLT(endOfDay),
		).
		First(ctx)
}

// CreateOrUpdate 创建或更新摘要，同一群组、同一发送者、同一日期不重复插入，已存在则更新内容
func (m *SummaryModel) CreateOrUpdate(ctx context.Context, data *SummaryData) (*ent.Summary, error) {
	existing, err := m.getByChatSenderAndDate(ctx, data.ChatID, data.SenderID, data.SummaryDate)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if existing != nil {
		update := m.client.UpdateOneID(existing.ID).
			SetContent(data.Content).
			SetSenderName(data.SenderName)
		if data.SenderUsername != nil {
			update.SetSenderUsername(*data.SenderUsername)
		} else {
			update.ClearSenderUsername()
		}
		if data.SenderNickname != nil {
			update.SetSenderNickname(*data.SenderNickname)
		} else {
			update.ClearSenderNickname()
		}
		return update.Save(ctx)
	}
	return m.Create(ctx, data)
}

// GetByDateAndChat 查询指定日期的摘要（统一使用 UTC）
func (m *SummaryModel) GetByDateAndChat(ctx context.Context, chatID int64, date time.Time) ([]*ent.Summary, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	return m.client.Query().
		Where(
			summary.ChatIDEQ(chatID),
			summary.SummaryDateGTE(startOfDay),
			summary.SummaryDateLT(endOfDay),
		).
		Order(summary.BySummaryDate()).
		All(ctx)
}
