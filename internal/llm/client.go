package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/sashabaranov/go-openai"
)

// openAIClientInterface 定义 OpenAI 客户端接口，便于测试
type openAIClientInterface interface {
	CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

type Client struct {
	config         *config.LLM
	openaiClient   openAIClientInterface
	maxInputTokens int
}

func NewClient(cfg *config.LLM) *Client {
	openaiConfig := openai.DefaultConfig(cfg.APIKey)
	openaiConfig.BaseURL = cfg.BaseURL

	client := &Client{
		config:         cfg,
		openaiClient:   openai.NewClientWithConfig(openaiConfig),
		maxInputTokens: cfg.MaxTokens - 2000, // 预留 2000 tokens 给 system prompt 和输出
	}

	return client
}

// estimateTokens 估算文本的 token 数量
func estimateTokens(text string) int {
	// 简单估算：中文约 1.5 token/字，英文约 1.3 token/词
	// 这里使用字符数 * 1.2 作为近似值
	chineseChars := 0
	englishWords := 0

	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff {
			chineseChars++
		} else if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			englishWords++
		}
	}

	// 英文词数估算（简单按空格分割）
	words := strings.Fields(text)
	englishWords = len(words)

	// 总 token 估算
	tokens := int(float64(chineseChars)*1.5 + float64(englishWords)*1.3)
	if tokens < len(text)/4 {
		// 如果估算值太小，使用字符数的 1/4 作为下限
		tokens = len(text) / 4
	}

	return tokens
}

// ChatMessage 群聊单条消息
type ChatMessage struct {
	MessageID  int64
	SenderID   int64
	SenderName string
	Text       string
}

// topicsSummaryJSON 用于解析 LLM 返回的话题分组 JSON
type topicsSummaryJSON struct {
	Topics []topicItemJSON `json:"topics"`
}

type topicItemJSON struct {
	Title string            `json:"title"`
	Items []topicSubItemJSON `json:"items"`
}

type topicSubItemJSON struct {
	SenderName  string  `json:"sender_name"`
	Description string  `json:"description"`
	MessageIDs  []int64 `json:"message_ids"`
}

// messagesToPromptText 将消息数组转为 prompt 文本，格式为每行 "[发送者名|msg_id] 消息内容"
func messagesToPromptText(msgs []ChatMessage) string {
	lines := make([]string, len(msgs))
	for i, m := range msgs {
		lines[i] = fmt.Sprintf("[%s|%d] %s", m.SenderName, m.MessageID, m.Text)
	}
	return strings.Join(lines, "\n")
}

// splitMessagesIntoChunks 将消息数组按 token 估算拆分为多个 chunk
func splitMessagesIntoChunks(msgs []ChatMessage, maxTokensPerChunk int) [][]ChatMessage {
	if len(msgs) == 0 {
		return nil
	}
	chunks := make([][]ChatMessage, 0)
	current := make([]ChatMessage, 0)
	currentTokens := 0

	for _, m := range msgs {
		line := fmt.Sprintf("[%s|%d] %s", m.SenderName, m.MessageID, m.Text)
		tokens := estimateTokens(line)
		if currentTokens+tokens > maxTokensPerChunk && len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
			currentTokens = 0
		}
		current = append(current, m)
		currentTokens += tokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// formatTopicsForContext 将话题摘要序列化为可读文本，用于多 chunk 增量合并时的上下文
func formatTopicsForContext(topics []topicItemJSON) string {
	var sb strings.Builder
	for i, t := range topics {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t.Title))
		for _, item := range t.Items {
			msgIDs := make([]string, len(item.MessageIDs))
			for j, id := range item.MessageIDs {
				msgIDs[j] = fmt.Sprintf("%d", id)
			}
			sb.WriteString(fmt.Sprintf("   - %s: %s (msg:%s)\n", item.SenderName, item.Description, strings.Join(msgIDs, ",")))
		}
	}
	return sb.String()
}

// mergeTopics 代码层兜底合并：将 partial 合并到 accumulated 中
// 按 topic title 匹配，同一话题同一 sender 的 message_ids 取并集
// 若旧话题在新结果中完全消失，原样保留
func mergeTopics(accumulated, partial *topicsSummaryJSON) *topicsSummaryJSON {
	if accumulated == nil {
		return partial
	}
	if partial == nil {
		return accumulated
	}

	// 建立旧话题 title -> index 的映射
	oldTopicMap := make(map[string]int)
	for i, t := range accumulated.Topics {
		oldTopicMap[t.Title] = i
	}

	// 用 accumulated 作为基础，逐个处理 partial 的话题
	result := &topicsSummaryJSON{
		Topics: make([]topicItemJSON, len(accumulated.Topics)),
	}
	copy(result.Topics, accumulated.Topics)

	for _, pt := range partial.Topics {
		if oldIdx, exists := oldTopicMap[pt.Title]; exists {
			// 同名话题：按 sender_name 合并 items
			result.Topics[oldIdx] = mergeTopicItems(result.Topics[oldIdx], pt)
		} else {
			// 新话题：直接追加
			result.Topics = append(result.Topics, pt)
		}
	}

	return result
}

// mergeTopicItems 合并同一话题下的 items，按 sender_name 去重并合并 message_ids
func mergeTopicItems(old, new topicItemJSON) topicItemJSON {
	merged := topicItemJSON{
		Title: new.Title,
		Items: make([]topicSubItemJSON, 0),
	}

	// 建立旧 items 的 sender_name -> index 映射
	oldItemMap := make(map[string]int)
	for i, item := range old.Items {
		oldItemMap[item.SenderName] = i
	}

	// 先复制旧 items
	merged.Items = append(merged.Items, old.Items...)

	// 处理新 items
	for _, newItem := range new.Items {
		if oldIdx, exists := oldItemMap[newItem.SenderName]; exists {
			// 同一 sender：合并 message_ids（取并集），更新 description
			mergedIDs := mergeMessageIDs(merged.Items[oldIdx].MessageIDs, newItem.MessageIDs)
			merged.Items[oldIdx] = topicSubItemJSON{
				SenderName:  newItem.SenderName,
				Description: newItem.Description,
				MessageIDs:  mergedIDs,
			}
		} else {
			// 新 sender：直接追加
			merged.Items = append(merged.Items, newItem)
		}
	}

	return merged
}

// mergeMessageIDs 合并两个 message_id 切片，去重
func mergeMessageIDs(a, b []int64) []int64 {
	seen := make(map[int64]bool)
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		seen[id] = true
	}
	result := make([]int64, 0, len(seen))
	// 保持顺序：先 a 中的，再 b 中新增的
	for _, id := range a {
		if seen[id] {
			result = append(result, id)
			delete(seen, id)
		}
	}
	for _, id := range b {
		if seen[id] {
			result = append(result, id)
			delete(seen, id)
		}
	}
	return result
}

// SummarizeChat 将群聊消息总结为话题分组 JSON
// 传入结构化的消息数组
// 返回完整的 JSON 字符串
func (c *Client) SummarizeChat(ctx context.Context, messages []ChatMessage) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}
	chatText := messagesToPromptText(messages)
	tokens := estimateTokens(chatText)

	if tokens <= c.maxInputTokens {
		return c.summarizeChatOnce(ctx, chatText, "")
	}

	// Token 超限，采用优化版增量拼接
	logger.Infof("[LLM] 群聊消息过长 (%d tokens)，将拆分为多个 chunk 进行总结", tokens)
	chunks := splitMessagesIntoChunks(messages, c.maxInputTokens)

	var accumulated *topicsSummaryJSON
	for i, chunkMsgs := range chunks {
		logger.Debugf("[LLM] 处理 chunk %d/%d", i+1, len(chunks))
		chunkText := messagesToPromptText(chunkMsgs)

		var prevTopics string
		if accumulated != nil {
			prevTopics = formatTopicsForContext(accumulated.Topics)
		}

		raw, err := c.summarizeChatOnce(ctx, chunkText, prevTopics)
		if err != nil {
			return "", fmt.Errorf("总结 chunk %d 失败: %w", i+1, err)
		}

		var partial topicsSummaryJSON
		if err := json.Unmarshal([]byte(raw), &partial); err != nil {
			return "", fmt.Errorf("解析 chunk %d 的 JSON 失败: %w", i+1, err)
		}

		// 代码层兜底合并
		accumulated = mergeTopics(accumulated, &partial)
	}

	data, _ := json.Marshal(accumulated)
	return string(data), nil
}

// summarizeChatOnce 执行一次群聊总结请求，返回 JSON 字符串
func (c *Client) summarizeChatOnce(ctx context.Context, chunkContent, prevTopicsSummary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	systemPrompt := `你是一个专业的群聊总结助手。根据用户提供的群聊内容，按话题分组总结，输出严格的 JSON 格式。

输入格式为每行 "[发言者名|消息ID] 消息内容"。

输出要求：
{
  "topics": [
    {
      "title": "话题标题（简洁概括）",
      "items": [
        {
          "sender_name": "发言者名",
          "description": "该发言者在此话题下的贡献总结",
          "message_ids": [对应的消息ID数组]
        }
      ]
    }
  ]
}

注意事项：
1. 按讨论话题归类，每个话题 2-4 条子项
2. sender_name 必须与输入中的发言者名完全一致
3. message_ids 返回该发言者在此话题下发言的最具代表性的 1-3 条消息ID（选择最能代表其贡献的关键消息）
4. description 应具体描述该发言者的观点或贡献
5. 话题数量控制在 5-15 个，按重要性排序
6. 只输出 JSON，不要其他内容`

	userPrompt := chunkContent
	if prevTopicsSummary != "" {
		userPrompt = "【上一轮已有话题总结，请在此基础上合并新内容后输出更新后的完整 JSON】\n\n"
		userPrompt += "上一轮话题总结：\n" + prevTopicsSummary + "\n\n"
		userPrompt += "新消息内容：\n" + chunkContent + "\n\n请输出更新后的完整 topics JSON（合并已有话题或新增话题，保留所有 message_ids）。"
	} else {
		userPrompt = "群聊内容：\n" + chunkContent + "\n\n请输出 JSON。"
	}

	req := openai.ChatCompletionRequest{
		Model: c.config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   4000,
	}

	resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("调用 LLM API 失败: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("LLM API 返回空结果")
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	return content, nil
}
