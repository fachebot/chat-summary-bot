package model

import (
	"context"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/ent"
	"github.com/fachebot/chat-summary-bot/internal/ent/message"
)

type MessageModel struct {
	client *ent.MessageClient
}

func NewMessageModel(client *ent.MessageClient) *MessageModel {
	return &MessageModel{client: client}
}

type MessageData struct {
	MessageID      int64
	ChatID         int64
	SenderID       int64
	SenderName     string
	SenderUsername *string
	Text           string
	SentAt         time.Time
}

// Create 创建消息
func (m *MessageModel) Create(ctx context.Context, data *MessageData) (*ent.Message, error) {
	create := m.client.Create().
		SetMessageID(data.MessageID).
		SetChatID(data.ChatID).
		SetSenderID(data.SenderID).
		SetSenderName(data.SenderName).
		SetText(data.Text).
		SetSentAt(data.SentAt)

	if data.SenderUsername != nil {
		create.SetSenderUsername(*data.SenderUsername)
	}
	return create.Save(ctx)
}

// GetByDateAndChat 按日期和群聊查询消息（统一使用 UTC）
func (m *MessageModel) GetByDateAndChat(ctx context.Context, chatID int64, date time.Time) ([]*ent.Message, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	return m.client.Query().
		Where(
			message.ChatIDEQ(chatID),
			message.SentAtGTE(startOfDay),
			message.SentAtLT(endOfDay),
		).
		Order(message.BySentAt()).
		All(ctx)
}

// GetSendersByDateAndChat 获取当日所有发言者（返回每个发送者的一条消息，用于获取发送者信息）（统一使用 UTC）
func (m *MessageModel) GetSendersByDateAndChat(ctx context.Context, chatID int64, date time.Time) ([]*ent.Message, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	// 获取所有消息，然后在应用层去重
	allMessages, err := m.client.Query().
		Where(
			message.ChatIDEQ(chatID),
			message.SentAtGTE(startOfDay),
			message.SentAtLT(endOfDay),
		).
		Order(message.BySentAt()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// 按 sender_id 去重，保留每个发送者的第一条消息
	senderMap := make(map[int64]*ent.Message)
	for _, msg := range allMessages {
		if _, exists := senderMap[msg.SenderID]; !exists {
			senderMap[msg.SenderID] = msg
		}
	}

	// 转换为切片
	result := make([]*ent.Message, 0, len(senderMap))
	for _, msg := range senderMap {
		result = append(result, msg)
	}

	return result, nil
}

// GetByDateRangeAndChat 查询时间区间内所有消息
func (m *MessageModel) GetByDateRangeAndChat(ctx context.Context, chatID int64, startTime, endTime time.Time) ([]*ent.Message, error) {
	return m.client.Query().
		Where(
			message.ChatIDEQ(chatID),
			message.SentAtGTE(startTime),
			message.SentAtLT(endTime),
		).
		Order(message.BySentAt()).
		All(ctx)
}

// GetSendersByDateRangeAndChat 获取时间区间内所有发言者
func (m *MessageModel) GetSendersByDateRangeAndChat(ctx context.Context, chatID int64, startTime, endTime time.Time) ([]*ent.Message, error) {
	allMessages, err := m.client.Query().
		Where(
			message.ChatIDEQ(chatID),
			message.SentAtGTE(startTime),
			message.SentAtLT(endTime),
		).
		Order(message.BySentAt()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	senderMap := make(map[int64]*ent.Message)
	for _, msg := range allMessages {
		if _, exists := senderMap[msg.SenderID]; !exists {
			senderMap[msg.SenderID] = msg
		}
	}

	result := make([]*ent.Message, 0, len(senderMap))
	for _, msg := range senderMap {
		result = append(result, msg)
	}
	return result, nil
}

// GetBySenderDateAndChat 获取指定发送者在指定日期的所有消息（统一使用 UTC）
func (m *MessageModel) GetBySenderDateAndChat(ctx context.Context, chatID int64, senderID int64, date time.Time) ([]*ent.Message, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	return m.client.Query().
		Where(
			message.ChatIDEQ(chatID),
			message.SenderIDEQ(senderID),
			message.SentAtGTE(startOfDay),
			message.SentAtLT(endOfDay),
		).
		Order(message.BySentAt()).
		All(ctx)
}

// GetChatIDsByDateRange 查询指定时间区间内有消息的所有群组ID
func (m *MessageModel) GetChatIDsByDateRange(ctx context.Context, startTime, endTime time.Time) ([]int64, error) {
	messages, err := m.client.Query().
		Where(
			message.SentAtGTE(startTime),
			message.SentAtLT(endTime),
		).
		Select(message.FieldChatID).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// 使用 map 去重
	chatIDMap := make(map[int64]bool)
	for _, msg := range messages {
		chatIDMap[msg.ChatID] = true
	}

	// 转换为切片
	chatIDs := make([]int64, 0, len(chatIDMap))
	for chatID := range chatIDMap {
		chatIDs = append(chatIDs, chatID)
	}

	return chatIDs, nil
}

// DeleteBefore 删除指定日期之前的消息
func (m *MessageModel) DeleteBefore(ctx context.Context, cutoffDate time.Time) (int, error) {
	return m.client.Delete().
		Where(message.SentAtLT(cutoffDate)).
		Exec(ctx)
}
