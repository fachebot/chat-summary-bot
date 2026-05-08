package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantMin int
		wantMax int
	}{
		{"空文本", "", 0, 0},
		{"纯中文", "这是一段中文测试文本", 8, 50},
		{"纯英文", "This is a test message", 4, 30},
		{"中英混合", "Hello 世界 test 测试", 4, 40},
		{"长文本", "这是一段很长的中文文本。" + "重复" + "重复" + "重复" + "重复" + "重复", 20, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.text)
			assert.GreaterOrEqual(t, got, tt.wantMin)
			assert.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestEstimateTokens_AppliesSafetyFactor(t *testing.T) {
	assert.Equal(t, 8, estimateTokens("This is a test message"))
	assert.Equal(t, 20, estimateTokens("这是一段中文测试文本"))
}

func TestMessagesToPromptText(t *testing.T) {
	msgs := []ChatMessage{
		{MessageID: 100, SenderID: 1, SenderName: "张三", Text: "你好"},
		{MessageID: 101, SenderID: 2, SenderName: "李四", Text: "大家好"},
	}
	got := messagesToPromptText(msgs)
	assert.Contains(t, got, "[张三|100] 你好")
	assert.Contains(t, got, "[李四|101] 大家好")
}

func TestMessagesToPromptText_Empty(t *testing.T) {
	got := messagesToPromptText(nil)
	assert.Empty(t, got)
}

func TestSplitMessagesIntoChunks(t *testing.T) {
	tests := []struct {
		name              string
		msgs              []ChatMessage
		maxTokensPerChunk int
		wantChunks        int
	}{
		{
			name: "短消息不分块",
			msgs: []ChatMessage{
				{MessageID: 1, SenderID: 1, SenderName: "A", Text: "短消息"},
			},
			maxTokensPerChunk: 1000,
			wantChunks:        1,
		},
		{
			name:              "空消息返回nil",
			msgs:              nil,
			maxTokensPerChunk: 100,
			wantChunks:        0,
		},
		{
			name: "多消息按 token 分块",
			msgs: func() []ChatMessage {
				var msgs []ChatMessage
				for i := 0; i < 20; i++ {
					msgs = append(msgs, ChatMessage{MessageID: int64(i), SenderID: int64(i), SenderName: "User", Text: "这是一条较长的中文测试消息内容"})
				}
				return msgs
			}(),
			maxTokensPerChunk: 50,
			wantChunks:        -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitMessagesIntoChunks(tt.msgs, tt.maxTokensPerChunk)
			if tt.wantChunks == 0 {
				assert.Nil(t, chunks)
				return
			}
			if tt.wantChunks > 0 {
				assert.Len(t, chunks, tt.wantChunks)
			} else if tt.wantChunks == -1 {
				assert.GreaterOrEqual(t, len(chunks), 2, "应拆分为多块")
			}
			total := 0
			for _, c := range chunks {
				total += len(c)
			}
			assert.Equal(t, len(tt.msgs), total, "总消息数应守恒")
		})
	}
}

func TestCalculateTokenBudgets(t *testing.T) {
	t.Run("使用显式输出预算", func(t *testing.T) {
		maxInputTokens, maxOutputTokens := calculateTokenBudgets(1_000_000, 384_000)
		assert.Equal(t, 614000, maxInputTokens)
		assert.Equal(t, 384000, maxOutputTokens)
	})

	t.Run("默认输出预算不会超过上下文", func(t *testing.T) {
		maxInputTokens, maxOutputTokens := calculateTokenBudgets(10000, 0)
		assert.Equal(t, 4000, maxInputTokens)
		assert.Equal(t, 4000, maxOutputTokens)
		assert.LessOrEqual(t, maxInputTokens+maxOutputTokens+defaultPromptReserveTokens, 10000)
	})
}
