package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mockOpenAIClient 模拟 OpenAI 客户端
type mockOpenAIClient struct {
	mock.Mock
}

func (m *mockOpenAIClient) CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(openai.ChatCompletionResponse), args.Error(1)
}

// newTestClient 创建用于测试的客户端，注入 mock
func newTestClient(cfg *config.LLM, mockClient openAIClientInterface) *Client {
	return newTestClientWithMaxTokens(cfg, mockClient, 0)
}

// newTestClientWithMaxTokens 可指定 maxInputTokens，0 表示使用自动计算的输入预算
func newTestClientWithMaxTokens(cfg *config.LLM, mockClient openAIClientInterface, maxInputTokens int) *Client {
	calculatedMaxInputTokens, maxOutputTokens := calculateTokenBudgets(cfg.MaxTokens, cfg.MaxOutputTokens)
	if maxInputTokens <= 0 {
		maxInputTokens = calculatedMaxInputTokens
	}
	return &Client{
		config:          cfg,
		openaiClient:    mockClient,
		totalTokens:     cfg.MaxTokens,
		maxInputTokens:  maxInputTokens,
		maxOutputTokens: maxOutputTokens,
	}
}

func TestSummarizeChat_EmptyMessages(t *testing.T) {
	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, &mockOpenAIClient{})

	result, err := client.SummarizeChat(context.Background(), nil)
	assert.NoError(t, err)
	assert.Empty(t, result)

	result, err = client.SummarizeChat(context.Background(), []ChatMessage{})
	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestSummarizeChat_Success(t *testing.T) {
	jsonResp := `{"topics":[{"title":"技术讨论","items":[{"sender_name":"张三","description":"分享了方案","message_ids":[100]},{"sender_name":"李四","description":"汇报进展","message_ids":[101]}]}]}`
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: jsonResp}},
			},
		}, nil)

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{
		{MessageID: 100, SenderID: 1, SenderName: "张三", Text: "分享了技术方案"},
		{MessageID: 101, SenderID: 2, SenderName: "李四", Text: "汇报了进展"},
	}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	mockAPI.AssertExpectations(t)

	var parsed topicsSummaryJSON
	err = json.Unmarshal([]byte(result), &parsed)
	assert.NoError(t, err)
	assert.Len(t, parsed.Topics, 1)
	assert.Equal(t, "技术讨论", parsed.Topics[0].Title)
	assert.Len(t, parsed.Topics[0].Items, 2)
	assert.Equal(t, "张三", parsed.Topics[0].Items[0].SenderName)
	assert.Equal(t, "分享了方案", parsed.Topics[0].Items[0].Description)
	assert.Equal(t, []int64{100}, parsed.Topics[0].Items[0].MessageIDs)
}

func TestSummarizeChat_APIError(t *testing.T) {
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openai.ChatCompletionResponse{}, errors.New("api error"))

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{{MessageID: 1, SenderID: 1, SenderName: "A", Text: "test"}}
	_, err := client.SummarizeChat(context.Background(), msgs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "调用 LLM API 失败")
}

func TestSummarizeChat_EmptyResponse(t *testing.T) {
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openai.ChatCompletionResponse{Choices: nil}, nil)

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{{MessageID: 1, SenderID: 1, SenderName: "A", Text: "test"}}
	_, err := client.SummarizeChat(context.Background(), msgs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "返回空结果")
}

func TestSummarizeChat_RetriesInvalidJSON(t *testing.T) {
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: "not valid json"}},
			},
		}, nil).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "上一次输出未通过校验")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: `{"topics":[{"title":"测试","items":[{"sender_name":"A","description":"x","message_ids":[1]}]}]}`}},
		},
	}, nil).Once()

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{{MessageID: 1, SenderID: 1, SenderName: "A", Text: "test"}}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)
	mockAPI.AssertExpectations(t)
}

func TestSummarizeChat_LongMessagesChunked(t *testing.T) {
	// 使用极小的 maxInputTokens 强制触发分块
	chunk1Resp := `{"topics":[{"title":"话题A","items":[{"sender_name":"A","description":"总结1","message_ids":[100]}]}]}`
	chunk2Resp := `{"topics":[{"title":"话题A","items":[{"sender_name":"B","description":"总结2","message_ids":[200]}]}]}`
	mergedResp := `{"topics":[{"title":"话题A","items":[{"sender_name":"A","description":"总结1","message_ids":[100]},{"sender_name":"B","description":"总结2","message_ids":[200]}]}]}`
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "群聊内容：") && strings.Contains(req.Messages[1].Content, "[A|100]")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: chunk1Resp}}},
	}, nil).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "群聊内容：") && strings.Contains(req.Messages[1].Content, "[B|200]")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: chunk2Resp}}},
	}, nil).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "以下是来自不同消息分块的话题摘要 JSON")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: mergedResp}}},
	}, nil).Once()

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClientWithMaxTokens(cfg, mockAPI, 30) // 很小，强制分块

	msgs := []ChatMessage{
		{MessageID: 100, SenderID: 1, SenderName: "A", Text: "第一条较长的中文消息内容"},
		{MessageID: 200, SenderID: 2, SenderName: "B", Text: "第二条较长的中文消息内容"},
	}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	mockAPI.AssertExpectations(t)

	var parsed topicsSummaryJSON
	err = json.Unmarshal([]byte(result), &parsed)
	assert.NoError(t, err)
	assert.Len(t, parsed.Topics, 1)
	assert.Equal(t, "话题A", parsed.Topics[0].Title)
	// 合并后应包含 A 和 B
	assert.Len(t, parsed.Topics[0].Items, 2)
}

func TestSummarizeChat_TrimsMarkdownCodeBlock(t *testing.T) {
	jsonResp := `{"topics":[{"title":"测试","items":[{"sender_name":"A","description":"x","message_ids":[1]}]}]}`
	wrapped := "```json\n" + jsonResp + "\n```"
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: wrapped}},
			},
		}, nil)

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{{MessageID: 1, SenderID: 1, SenderName: "A", Text: "x"}}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	var parsed topicsSummaryJSON
	err = json.Unmarshal([]byte(result), &parsed)
	assert.NoError(t, err)
	assert.Len(t, parsed.Topics, 1)
}

func TestSummarizeChat_UsesConfiguredMaxOutputTokens(t *testing.T) {
	jsonResp := `{"topics":[{"title":"测试","items":[{"sender_name":"A","description":"x","message_ids":[1]}]}]}`
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return req.MaxTokens == 384000 && req.ResponseFormat != nil && req.ResponseFormat.Type == openai.ChatCompletionResponseFormatTypeJSONObject
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: jsonResp}},
		},
	}, nil).Once()

	cfg := &config.LLM{Model: "deepseek-v4", MaxTokens: 1_000_000, MaxOutputTokens: 384_000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{{MessageID: 1, SenderID: 1, SenderName: "A", Text: "x"}}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)
	mockAPI.AssertExpectations(t)
}

func TestSummarizeChat_ReducesOutputBudgetOnContextLimit(t *testing.T) {
	jsonResp := `{"topics":[{"title":"测试","items":[{"sender_name":"A","description":"x","message_ids":[1]}]}]}`
	contextLimitErr := &openai.APIError{Message: "maximum context length exceeded", HTTPStatusCode: 400, Type: "invalid_request_error"}
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return req.MaxTokens == 4000
	})).Return(openai.ChatCompletionResponse{}, contextLimitErr).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return req.MaxTokens == 2000
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: jsonResp}}},
	}, nil).Once()

	cfg := &config.LLM{Model: "test", MaxTokens: 10000}
	client := newTestClient(cfg, mockAPI)

	msgs := []ChatMessage{{MessageID: 1, SenderID: 1, SenderName: "A", Text: "test"}}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)
	mockAPI.AssertExpectations(t)
}

func TestSummarizeChat_FallsBackToSmallerChunksOnContextLimit(t *testing.T) {
	contextLimitErr := &openai.APIError{Message: "maximum context length exceeded", HTTPStatusCode: 400, Type: "invalid_request_error"}
	chunk1Resp := `{"topics":[{"title":"话题A","items":[{"sender_name":"A","description":"总结1","message_ids":[100]}]}]}`
	chunk2Resp := `{"topics":[{"title":"话题A","items":[{"sender_name":"B","description":"总结2","message_ids":[200]}]}]}`
	mergedResp := `{"topics":[{"title":"话题A","items":[{"sender_name":"A","description":"总结1","message_ids":[100]},{"sender_name":"B","description":"总结2","message_ids":[200]}]}]}`
	mockAPI := new(mockOpenAIClient)
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "[A|100]") && strings.Contains(req.Messages[1].Content, "[B|200]") && req.MaxTokens == 4
	})).Return(openai.ChatCompletionResponse{}, contextLimitErr).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "[A|100]") && strings.Contains(req.Messages[1].Content, "[B|200]") && req.MaxTokens == 2
	})).Return(openai.ChatCompletionResponse{}, contextLimitErr).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "[A|100]") && strings.Contains(req.Messages[1].Content, "[B|200]") && req.MaxTokens == 1
	})).Return(openai.ChatCompletionResponse{}, contextLimitErr).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "群聊内容：") && strings.Contains(req.Messages[1].Content, "[A|100]") && !strings.Contains(req.Messages[1].Content, "[B|200]")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: chunk1Resp}}},
	}, nil).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "群聊内容：") && strings.Contains(req.Messages[1].Content, "[B|200]") && !strings.Contains(req.Messages[1].Content, "[A|100]")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: chunk2Resp}}},
	}, nil).Once()
	mockAPI.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openai.ChatCompletionRequest) bool {
		return strings.Contains(req.Messages[1].Content, "以下是来自不同消息分块的话题摘要 JSON")
	})).Return(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: mergedResp}}},
	}, nil).Once()

	cfg := &config.LLM{Model: "test", MaxTokens: 10000, MaxOutputTokens: 4}
	client := newTestClientWithMaxTokens(cfg, mockAPI, 100)

	msgs := []ChatMessage{
		{MessageID: 100, SenderID: 1, SenderName: "A", Text: "第一条较长的中文消息内容"},
		{MessageID: 200, SenderID: 2, SenderName: "B", Text: "第二条较长的中文消息内容"},
	}
	result, err := client.SummarizeChat(context.Background(), msgs)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)
	mockAPI.AssertExpectations(t)
}
