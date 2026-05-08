package llm

import (
	"context"
	"encoding/json"
	"errors"
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
	config          *config.LLM
	openaiClient    openAIClientInterface
	totalTokens     int
	maxInputTokens  int
	maxOutputTokens int
}

type topicsRequestSpec struct {
	taskName          string
	systemPrompt      string
	userPrompt        string
	allowedMessageIDs map[int64]struct{}
	allowedSenders    map[string]struct{}
}

const (
	defaultPromptReserveTokens = 2000
	minimumChunkInputTokens    = 1000
	minimumOutputTokens        = 1
	maximumDefaultOutputTokens = 32000
	tokenEstimateSafetyFactor  = 1.2
	defaultLLMRetryAttempts    = 3
	maxContextLimitShrinkRetry = 8
	minimumMergeBatchTokens    = 2000
	minimumTopicMatchScore     = 0.60
	retryBackoffBase           = 200 * time.Millisecond
)

const jsonObjectFormatHint = `JSON 结构模板（必须严格遵守）：
{
	"topics": [
		{
			"title": "话题标题",
			"items": [
				{
					"sender_name": "发言者名",
					"description": "贡献总结",
					"message_ids": [1234567890]
				}
			]
		}
	]
}

额外约束：
1. topics 数组里的每个元素只能包含 title 和 items 两个字段
2. 不要使用 topic 作为字段名；如果你想表达话题标题，字段名必须是 title
3. sender_name、description、message_ids 只能出现在 items 数组的元素里，不能直接出现在 topics 数组元素里
4. message_ids 必须是数字数组，不要写成字符串数组
5. 即使某个话题只有一个发言者，也必须把该发言者放进 items 数组`

func NewClient(cfg *config.LLM) *Client {
	openaiConfig := openai.DefaultConfig(cfg.APIKey)
	openaiConfig.BaseURL = cfg.BaseURL
	maxInputTokens, maxOutputTokens := calculateTokenBudgets(cfg.MaxTokens, cfg.MaxOutputTokens)

	client := &Client{
		config:          cfg,
		openaiClient:    openai.NewClientWithConfig(openaiConfig),
		totalTokens:     cfg.MaxTokens,
		maxInputTokens:  maxInputTokens,
		maxOutputTokens: maxOutputTokens,
	}

	logger.Infof("[LLM] 初始化客户端: model=%s, context=%d, input_budget=%d, output_budget=%d", cfg.Model, cfg.MaxTokens, maxInputTokens, maxOutputTokens)

	return client
}

func isContextLimitError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPStatusCode == 400 || apiErr.HTTPStatusCode == 413 {
			errMsg := strings.ToLower(apiErr.Message)
			if strings.Contains(errMsg, "context length") ||
				strings.Contains(errMsg, "maximum context") ||
				strings.Contains(errMsg, "context window") ||
				strings.Contains(errMsg, "too many tokens") ||
				strings.Contains(errMsg, "prompt is too long") ||
				strings.Contains(errMsg, "maximum allowed tokens") ||
				strings.Contains(errMsg, "context_length_exceeded") {
				return true
			}
		}
	}

	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "context length") ||
		strings.Contains(errMsg, "maximum context") ||
		strings.Contains(errMsg, "context window") ||
		strings.Contains(errMsg, "too many tokens") ||
		strings.Contains(errMsg, "prompt is too long") ||
		strings.Contains(errMsg, "maximum allowed tokens") ||
		strings.Contains(errMsg, "context_length_exceeded")
}

func nextReducedOutputLimit(current int) int {
	if current <= minimumOutputTokens {
		return current
	}

	next := current / 2
	if next >= current {
		next = current - 1
	}
	if next < minimumOutputTokens {
		next = minimumOutputTokens
	}
	return next
}

func nextReducedChunkLimit(current int) int {
	if current <= 1 {
		return 0
	}

	next := current / 2
	if next >= current {
		next = current - 1
	}
	if next < 1 {
		next = 1
	}
	return next
}

func shouldRetryLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isContextLimitError(err) {
		return false
	}
	if strings.Contains(err.Error(), "请求 token 预算不足") {
		return false
	}
	return true
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func buildResponseFormat() *openai.ChatCompletionResponseFormat {
	return &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject}
}

func (c *Client) callStructuredChatCompletion(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int) (string, error) {
	outputLimit := maxOutputTokens
	if outputLimit <= 0 || outputLimit > c.maxOutputTokens {
		outputLimit = c.maxOutputTokens
	}
	promptTokens := estimateTokens(systemPrompt) + estimateTokens(userPrompt)
	remainingTokens := c.totalTokens - promptTokens - defaultPromptReserveTokens
	if remainingTokens < outputLimit {
		outputLimit = remainingTokens
	}
	if outputLimit < minimumOutputTokens {
		return "", fmt.Errorf("请求 token 预算不足: prompt=%d, context=%d", promptTokens, c.totalTokens)
	}

	for shrinkAttempt := 0; ; shrinkAttempt++ {
		req := openai.ChatCompletionRequest{
			Model: c.config.Model,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: userPrompt},
			},
			Temperature:    0.1,
			MaxTokens:      outputLimit,
			ResponseFormat: buildResponseFormat(),
		}

		resp, err := c.openaiClient.CreateChatCompletion(ctx, req)
		if err != nil {
			if isContextLimitError(err) && shrinkAttempt < maxContextLimitShrinkRetry {
				nextOutputLimit := nextReducedOutputLimit(outputLimit)
				if nextOutputLimit < outputLimit {
					logger.Warnf("[LLM] 检测到上下文超限，自动收缩输出预算后重试: %d -> %d", outputLimit, nextOutputLimit)
					outputLimit = nextOutputLimit
					continue
				}
			}
			return "", fmt.Errorf("调用 LLM API 失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("LLM API 返回空结果")
		}

		return trimResponseContent(resp.Choices[0].Message.Content), nil
	}
}

func (c *Client) requestTopicsSummary(ctx context.Context, spec topicsRequestSpec) (*topicsSummaryJSON, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var lastErr error
	requestMaxOutputTokens := c.maxOutputTokens

	for attempt := 1; attempt <= defaultLLMRetryAttempts; attempt++ {
		userPrompt := spec.userPrompt
		userPrompt += "\n\n" + jsonObjectFormatHint
		if attempt > 1 {
			userPrompt += "\n\n上一次输出未通过校验。请重新输出，必须满足：1. 只返回一个合法 JSON 对象；2. 不要 Markdown 代码块和任何解释；3. 只能使用输入中已有的 sender_name 和 message_ids；4. 不得输出额外字段；5. topics 必须非空。"
		}

		raw, err := c.callStructuredChatCompletion(ctx, spec.systemPrompt, userPrompt, requestMaxOutputTokens)
		if err == nil {
			summary, parseErr := parseTopicsSummary(raw, spec.allowedMessageIDs, spec.allowedSenders)
			if parseErr == nil {
				return summary, nil
			}
			lastErr = parseErr
			logger.Warnf("[LLM] %s 第 %d 次输出校验失败: %v", spec.taskName, attempt, parseErr)
		} else {
			lastErr = err
			logger.Warnf("[LLM] %s 第 %d 次请求失败: %v", spec.taskName, attempt, err)
		}

		if attempt == defaultLLMRetryAttempts || !shouldRetryLLMError(lastErr) {
			break
		}
		if err := sleepWithContext(ctx, time.Duration(attempt)*retryBackoffBase); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("%s失败: %w", spec.taskName, lastErr)
}

func (c *Client) summarizeChatInChunks(ctx context.Context, messages []ChatMessage, maxTokensPerChunk int) (*topicsSummaryJSON, error) {
	if maxTokensPerChunk <= 0 {
		maxTokensPerChunk = 1
	}

	chatText := messagesToPromptText(messages)
	tokens := estimateTokens(chatText)
	logger.Infof("[LLM] 群聊消息过长 (%d tokens)，将按 %d tokens/chunk 进行总结并分层归并", tokens, maxTokensPerChunk)
	chunks := splitMessagesIntoChunks(messages, maxTokensPerChunk)

	partialSummaries := make([]*topicsSummaryJSON, 0, len(chunks))
	for i, chunkMsgs := range chunks {
		logger.Debugf("[LLM] 处理 chunk %d/%d", i+1, len(chunks))
		summary, err := c.summarizeMessagesAdaptive(ctx, chunkMsgs, maxTokensPerChunk)
		if err != nil {
			return nil, fmt.Errorf("总结 chunk %d 失败: %w", i+1, err)
		}
		partialSummaries = append(partialSummaries, summary)
	}

	mergedSummary, err := c.mergeChunkSummaries(ctx, partialSummaries)
	if err != nil {
		return nil, fmt.Errorf("归并分块总结失败: %w", err)
	}

	return mergedSummary, nil
}

func (c *Client) summarizeMessagesAdaptive(ctx context.Context, messages []ChatMessage, maxTokensPerChunk int) (*topicsSummaryJSON, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	if maxTokensPerChunk <= 0 {
		maxTokensPerChunk = 1
	}

	chatText := messagesToPromptText(messages)
	tokens := estimateTokens(chatText)
	if tokens > maxTokensPerChunk {
		if len(messages) == 1 {
			return c.requestTopicsSummary(ctx, buildSummarizeMessagesSpec(messages))
		}
		return c.summarizeChatInChunks(ctx, messages, maxTokensPerChunk)
	}

	summary, err := c.requestTopicsSummary(ctx, buildSummarizeMessagesSpec(messages))
	if err == nil {
		return summary, nil
	}

	if isContextLimitError(err) && len(messages) > 1 {
		nextChunkLimit := nextReducedChunkLimit(tokens)
		if nextChunkLimit > 0 {
			logger.Warnf("[LLM] 单次总结命中上下文限制，改用更小 chunk 预算 %d 继续重试: %v", nextChunkLimit, err)
			return c.summarizeChatInChunks(ctx, messages, nextChunkLimit)
		}
	}

	return nil, err
}

func buildSummarizeMessagesSpec(messages []ChatMessage) topicsRequestSpec {
	allowedMessageIDs, allowedSenders := buildAllowedSetsFromMessages(messages)
	chunkContent := messagesToPromptText(messages)

	return topicsRequestSpec{
		taskName:          "总结消息分块",
		systemPrompt:      `你是一个专业的群聊总结助手。根据用户提供的群聊内容，按话题分组总结，输出严格的 JSON。\n\n输入格式为每行 "[发言者名|消息ID] 消息内容"。\n\n输出要求：\n1. 只返回一个 JSON object，不要 Markdown 代码块，不要解释\n2. sender_name 必须与输入中的发言者名完全一致\n3. message_ids 只能使用输入中出现过的消息ID，每个 item 保留 1-3 个最具代表性的消息ID\n4. description 必须具体描述该发言者在该话题下的观点或贡献\n5. 话题数量控制在 5-15 个，按重要性排序\n6. 不要输出任何额外字段`,
		userPrompt:        "群聊内容：\n" + chunkContent + "\n\n请只返回合法 JSON 对象。",
		allowedMessageIDs: allowedMessageIDs,
		allowedSenders:    allowedSenders,
	}
}

func buildMergeSummariesSpec(summaries []*topicsSummaryJSON) topicsRequestSpec {
	allowedMessageIDs, allowedSenders := buildAllowedSetsFromSummaries(summaries)
	batchContent := formatSummaryBatchForPrompt(summaries)

	return topicsRequestSpec{
		taskName:          "归并分块摘要",
		systemPrompt:      `你是一个群聊话题归并助手。输入是多个分块摘要 JSON，它们已经符合统一结构。请将相同或高度相关的话题归并成更稳定的完整 JSON。\n\n归并要求：\n1. 同一主题即使标题略有不同，也要合并为一个更稳定的标题\n2. sender_name 只能使用输入摘要中已有的名字，且必须逐字一致\n3. message_ids 只能使用输入摘要中已有的消息ID，且需要尽量保留全部有效 ID\n4. 删除重复或空洞的话题，避免相同主题重复出现\n5. 只返回一个 JSON object，不要 Markdown 代码块，不要解释，不要额外字段`,
		userPrompt:        "以下是来自不同消息分块的话题摘要 JSON。请将它们归并为一个完整 JSON：\n\n" + batchContent + "\n\n请只返回归并后的合法 JSON 对象。",
		allowedMessageIDs: allowedMessageIDs,
		allowedSenders:    allowedSenders,
	}
}

func (c *Client) mergeChunkSummaries(ctx context.Context, summaries []*topicsSummaryJSON) (*topicsSummaryJSON, error) {
	if len(summaries) == 0 {
		return nil, nil
	}
	if len(summaries) == 1 {
		return summaries[0], nil
	}

	mergeBudget := c.maxInputTokens
	if mergeBudget < minimumMergeBatchTokens {
		mergeBudget = minimumMergeBatchTokens
	}

	current := summaries
	for round := 1; len(current) > 1; round++ {
		batches := splitSummaryBatchesForMerge(current, mergeBudget)
		if len(batches) == len(current) {
			return mergeSummaryBatchFallback(current), nil
		}

		nextRound := make([]*topicsSummaryJSON, 0, len(batches))
		for batchIndex, batch := range batches {
			if len(batch) == 1 {
				nextRound = append(nextRound, batch[0])
				continue
			}

			summary, err := c.requestTopicsSummary(ctx, buildMergeSummariesSpec(batch))
			if err != nil {
				logger.Warnf("[LLM] 第 %d 轮归并 batch %d/%d 失败，回退到本地合并: %v", round, batchIndex+1, len(batches), err)
				summary = mergeSummaryBatchFallback(batch)
			}
			nextRound = append(nextRound, summary)
		}
		current = nextRound
	}

	return current[0], nil
}

// SummarizeChat 将群聊消息总结为话题分组 JSON
// 传入结构化的消息数组
// 返回完整的 JSON 字符串
func (c *Client) SummarizeChat(ctx context.Context, messages []ChatMessage) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}
	summary, err := c.summarizeMessagesAdaptive(ctx, messages, c.maxInputTokens)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("序列化总结结果失败: %w", err)
	}
	return string(data), nil
}
