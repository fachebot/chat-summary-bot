package summarizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/ent"
	"github.com/fachebot/chat-summary-bot/internal/llm"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/model"
)

// messageProvider 获取时间区间内的消息（便于测试注入 mock）
type messageProvider interface {
	GetByDateRangeAndChat(ctx context.Context, chatID int64, startTime, endTime time.Time) ([]*ent.Message, error)
}

// llmSummarizer 调用 LLM 总结群聊（便于测试注入 mock）
type llmSummarizer interface {
	SummarizeChat(ctx context.Context, messages []llm.ChatMessage) (string, error)
}

type Summarizer struct {
	llmClient    llmSummarizer
	messageModel messageProvider
	botUserID    int64
}

func NewSummarizer(llmClient *llm.Client, messageModel *model.MessageModel, botUserID int64) *Summarizer {
	return &Summarizer{
		llmClient:    llmClient,
		messageModel: messageModel,
		botUserID:    botUserID,
	}
}

// toLinkMessageID 将 TDLib 的 message_id 转为 t.me 链接用逻辑 ID（大 ID >>20，小 ID 不变）
const tdlibInternalIDThreshold = 1 << 30

func toLinkMessageID(messageID int64) int64 {
	if messageID >= tdlibInternalIDThreshold {
		return int64(uint64(messageID) >> 20)
	}
	return messageID
}

// escapeHTML 对文本进行 HTML 转义，防止注入及破坏标签
// 转义：& < > "
func escapeHTML(text string) string {
	result := strings.ReplaceAll(text, "&", "&amp;")
	result = strings.ReplaceAll(result, "<", "&lt;")
	result = strings.ReplaceAll(result, ">", "&gt;")
	result = strings.ReplaceAll(result, "\"", "&quot;")
	return result
}

// isSummaryMessage 判断消息是否为机器人发送的总结消息
func isSummaryMessage(text string) bool {
	return strings.HasPrefix(text, "📊 群组总结") || strings.HasPrefix(text, "📊 BTC抄底指标")
}

// SummarizeRange 生成指定时间区间的群聊总结
func (s *Summarizer) SummarizeRange(ctx context.Context, chatID int64, startTime, endTime time.Time) (*SummaryResult, error) {
	startStr := startTime.Format("2006-01-02")
	endStr := endTime.Format("2006-01-02")
	logger.Infof("[Summarizer] 开始生成 %s ~ %s 的群聊总结", startStr, endStr)

	messages, err := s.messageModel.GetByDateRangeAndChat(ctx, chatID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("获取消息失败: %w", err)
	}

	if len(messages) == 0 {
		logger.Infof("[Summarizer] 区间内无消息，跳过总结")
		return nil, nil
	}

	logger.Infof("[Summarizer] 找到 %d 条消息", len(messages))

	// 过滤掉机器人自己发送的总结消息（通过消息内容特征判断）
	filtered := make([]*ent.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.SenderID == s.botUserID && isSummaryMessage(msg.Text) {
			continue
		}
		filtered = append(filtered, msg)
	}
	messages = filtered
	logger.Infof("[Summarizer] 过滤机器人总结消息后剩余 %d 条消息", len(messages))

	if len(messages) == 0 {
		logger.Infof("[Summarizer] 过滤后无消息，跳过总结")
		return nil, nil
	}

	// 转换为结构化消息数组；提交给 LLM 前将 message_id 转为链接用短 ID
	chatMsgs := make([]llm.ChatMessage, len(messages))
	for i, msg := range messages {
		chatMsgs[i] = llm.ChatMessage{
			MessageID:  toLinkMessageID(msg.MessageID),
			SenderID:   msg.SenderID,
			SenderName: msg.SenderName,
			Text:       msg.Text,
		}
	}

	// 调用 LLM 总结
	jsonStr, err := s.llmClient.SummarizeChat(ctx, chatMsgs)
	if err != nil {
		return nil, fmt.Errorf("LLM 总结失败: %w", err)
	}

	var result SummaryResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		logger.Debugf("[Summarizer] 解析 LLM 返回的 JSON 失败: %s", jsonStr)
		return nil, fmt.Errorf("解析 LLM 返回的 JSON 失败: %w", err)
	}

	logger.Infof("[Summarizer] 完成总结，共 %d 个话题", len(result.Topics))
	return &result, nil
}

// buildMessageLink 构造 Telegram 超级群组消息链接
// 调用方应传入已转换的链接用短 message_id（参见 toLinkMessageID）
// TDLib 超级群组 chat_id 格式为 -100XXXXXXXXXX，channel_id = -chat_id - 1000000000000
func buildMessageLink(chatID int64, messageID int64) string {
	channelID := -chatID - 1000000000000
	if channelID <= 0 {
		// 非超级群组，返回空
		return ""
	}
	return fmt.Sprintf("https://t.me/c/%d/%d", channelID, messageID)
}

// buildGroupLink 构造 Telegram 超级群组链接
func buildGroupLink(chatID int64) string {
	channelID := -chatID - 1000000000000
	if channelID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://t.me/c/%d", channelID)
}

// FormatSummaryForDisplay 将 SummaryResult 格式化为目标样式的 HTML 文本
// 使用 Telegram HTML 语法：<b>粗体</b>、<a href="url">link</a>
func FormatSummaryForDisplay(result *SummaryResult, chatID int64, chatTitle, startDate, endDate string) string {
	if result == nil || len(result.Topics) == 0 {
		return ""
	}

	var sb strings.Builder

	// 头部
	sb.WriteString("📊 <b>群组总结</b>\n")
	if groupLink := buildGroupLink(chatID); groupLink != "" {
		sb.WriteString(fmt.Sprintf("🏠 <a href=\"%s\">%s</a>\n", escapeHTML(groupLink), escapeHTML(chatTitle)))
	} else {
		sb.WriteString(fmt.Sprintf("🏠 %s\n", escapeHTML(chatTitle)))
	}
	sb.WriteString(fmt.Sprintf("📅 %s 至 %s (UTC)\n", escapeHTML(startDate), escapeHTML(endDate)))

	// 话题列表（用户内容需 HTML 转义）
	for i, topic := range result.Topics {
		sb.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, escapeHTML(topic.Title)))
		for _, item := range topic.Items {
			sb.WriteString(fmt.Sprintf("- <b>%s</b> %s", escapeHTML(item.SenderName), escapeHTML(item.Description)))
			for _, msgID := range item.MessageIDs {
				link := buildMessageLink(chatID, msgID)
				if link != "" {
					sb.WriteString(fmt.Sprintf(" [<a href=\"%s\">link</a>]", escapeHTML(link)))
				}
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
