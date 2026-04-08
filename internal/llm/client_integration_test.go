package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// integrationTestConfig 从环境变量构建测试配置，若 LLM_API_KEY 未设置则跳过
func integrationTestConfig(t *testing.T) *config.LLM {
	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" || apiKey == "your-api-key-here" {
		t.Skip("跳过集成测试：请设置 LLM_API_KEY 环境变量")
	}
	baseURL := os.Getenv("LLM_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "gpt-3.5-turbo"
	}
	return &config.LLM{
		APIKey:    apiKey,
		BaseURL:   baseURL,
		Model:     model,
		MaxTokens: 16000,
	}
}

func TestSummarizeChat_Integration(t *testing.T) {
	cfg := integrationTestConfig(t)
	client := NewClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	msgs := []ChatMessage{
		{MessageID: 1001, SenderID: 1, SenderName: "张三", Text: "大家下午好，我们来同步一下本周进度"},
		{MessageID: 1002, SenderID: 2, SenderName: "李四", Text: "好的，我这边前端页面基本完成了，还差几个接口联调"},
		{MessageID: 1003, SenderID: 3, SenderName: "王五", Text: "后端 API 已经开发完了，文档也更新到 swagger 了"},
		{MessageID: 1004, SenderID: 1, SenderName: "张三", Text: "不错，李四你明天跟王五对接一下，把接口串起来"},
		{MessageID: 1005, SenderID: 2, SenderName: "李四", Text: "行，我上午找他"},
		{MessageID: 1006, SenderID: 4, SenderName: "赵六", Text: "测试环境我部署好了，你们联调完告诉我，我安排回归测试"},
		{MessageID: 1007, SenderID: 1, SenderName: "张三", Text: "好，我们争取周四前完成联调，周五留给测试"},
		{MessageID: 1008, SenderID: 3, SenderName: "王五", Text: "有个问题，用户登录那块需要加个验证码，可能要多半天"},
		{MessageID: 1009, SenderID: 1, SenderName: "张三", Text: "可以，你评估一下，如果时间紧就跟我说，咱们看能不能砍掉一些非核心需求"},
		{MessageID: 1010, SenderID: 2, SenderName: "李四", Text: "收到，大家加油"},
	}

	result, err := client.SummarizeChat(ctx, msgs)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	var parsed topicsSummaryJSON
	err = json.Unmarshal([]byte(result), &parsed)
	require.NoError(t, err, "返回内容应是合法 JSON: %s", result)

	assert.GreaterOrEqual(t, len(parsed.Topics), 1, "应有至少一个话题")

	// 输出总结内容
	t.Log("\n--- 话题总结 ---")
	for i, topic := range parsed.Topics {
		t.Logf("%d. %s", i+1, topic.Title)
		for _, item := range topic.Items {
			t.Logf("   - %s: %s (msgs: %v)", item.SenderName, item.Description, item.MessageIDs)
		}
	}
}

func TestSummarizeChat_Integration_EmptyMessages(t *testing.T) {
	cfg := integrationTestConfig(t)
	client := NewClient(cfg)
	ctx := context.Background()

	result, err := client.SummarizeChat(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, result)

	result, err = client.SummarizeChat(ctx, []ChatMessage{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestSummarizeChat_Integration_SingleMessage(t *testing.T) {
	cfg := integrationTestConfig(t)
	client := NewClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	msgs := []ChatMessage{
		{MessageID: 2001, SenderID: 100, SenderName: "测试用户", Text: "这是一条单条消息的测试"},
	}

	result, err := client.SummarizeChat(ctx, msgs)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	var parsed topicsSummaryJSON
	err = json.Unmarshal([]byte(result), &parsed)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(parsed.Topics), 1, "应有至少一个话题")
}
