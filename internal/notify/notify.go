package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/zelenin/go-tdlib/client"
)

const (
	MaxMessageLength = 5000 // Telegram 消息最大长度
)

type Notifier struct {
	tdClient *client.Client
	config   *config.Summary
}

func NewNotifier(tdClient *client.Client, cfg *config.Summary) *Notifier {
	return &Notifier{
		tdClient: tdClient,
		config:   cfg,
	}
}

// Notify 发送通知
// chatID 用于群组通知模式，当 NotifyMode 为 "group" 或 "both" 时使用
func (n *Notifier) Notify(ctx context.Context, content string, chatID int64) error {
	if content == "" {
		return nil
	}

	switch n.config.NotifyMode {
	case "private":
		return n.notifyPrivate(ctx, content)
	case "group":
		return n.notifyGroup(ctx, content, chatID)
	case "both":
		if err := n.notifyPrivate(ctx, content); err != nil {
			logger.Errorf("[Notify] 私信通知失败: %v", err)
		}
		if err := n.notifyGroup(ctx, content, chatID); err != nil {
			logger.Errorf("[Notify] 群发通知失败: %v", err)
		}
		return nil
	default:
		logger.Warnf("[Notify] 未知的通知模式: %s", n.config.NotifyMode)
		return nil
	}
}

// notifyPrivate 发送私信通知
func (n *Notifier) notifyPrivate(ctx context.Context, content string) error {
	if len(n.config.NotifyUserIds) == 0 {
		logger.Warnf("[Notify] 未配置私信通知用户ID")
		return nil
	}

	messages := n.splitMessage(content)

	for _, userID := range n.config.NotifyUserIds {
		for _, msg := range messages {
			formatted := n.parseHTMLText(msg)
			_, err := n.tdClient.SendMessage(&client.SendMessageRequest{
				ChatId: userID,
				InputMessageContent: &client.InputMessageText{
					Text: formatted,
				},
			})
			if err != nil {
				return fmt.Errorf("发送私信给用户 %d 失败: %w", userID, err)
			}
			logger.Infof("[Notify] 已发送私信给用户 %d", userID)
		}
	}

	return nil
}

// notifyGroup 发送群聊通知
func (n *Notifier) notifyGroup(ctx context.Context, content string, chatID int64) error {
	messages := n.splitMessage(content)

	for _, msg := range messages {
		formatted := n.parseHTMLText(msg)

		_, err := n.tdClient.SendMessage(&client.SendMessageRequest{
			ChatId: chatID,
			InputMessageContent: &client.InputMessageText{
				Text: formatted,
			},
		})
		if err != nil {
			return fmt.Errorf("发送群聊消息到群组 %d 失败: %w", chatID, err)
		}
		logger.Infof("[Notify] 已发送群聊消息到群组 %d", chatID)
	}

	return nil
}

// parseHTMLText 使用 TDLib 的 HTML 解析能力，将 HTML 文本转换为带实体的 FormattedText。
// 支持的 HTML 标签：<b>粗体</b>、<a href="url">链接</a>
func (n *Notifier) parseHTMLText(text string) *client.FormattedText {
	if text == "" {
		return &client.FormattedText{Text: text}
	}

	formatted, err := client.ParseTextEntities(&client.ParseTextEntitiesRequest{
		Text:      text,
		ParseMode: &client.TextParseModeHTML{},
	})
	if err != nil {
		logger.Warnf("[Notify] 解析 HTML 文本失败，回退为纯文本发送: %v", err)
		return &client.FormattedText{Text: text}
	}
	return formatted
}

// splitMessage 将消息按长度拆分为多条
func (n *Notifier) splitMessage(content string) []string {
	if len(content) <= MaxMessageLength {
		return []string{content}
	}

	// 按段落拆分
	paragraphs := strings.Split(content, "\n\n")
	if len(paragraphs) == 1 {
		// 如果没有段落分隔，按换行拆分
		paragraphs = strings.Split(content, "\n")
	}

	messages := make([]string, 0)
	currentMsg := ""

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		testMsg := currentMsg
		if testMsg != "" {
			testMsg += "\n\n"
		}
		testMsg += para

		if len(testMsg) <= MaxMessageLength {
			currentMsg = testMsg
		} else {
			// 当前消息已满，保存并开始新消息
			if currentMsg != "" {
				messages = append(messages, currentMsg)
			}
			// 如果单个段落就超过长度，需要进一步拆分
			if len(para) > MaxMessageLength {
				// 按句子拆分
				sentences := strings.Split(para, "。")
				for _, sentence := range sentences {
					sentence = strings.TrimSpace(sentence)
					if sentence == "" {
						continue
					}
					if len(currentMsg)+len(sentence)+2 > MaxMessageLength {
						if currentMsg != "" {
							messages = append(messages, currentMsg)
							currentMsg = ""
						}
					}
					if currentMsg != "" {
						currentMsg += "。"
					}
					currentMsg += sentence
				}
			} else {
				currentMsg = para
			}
		}
	}

	if currentMsg != "" {
		messages = append(messages, currentMsg)
	}

	return messages
}
