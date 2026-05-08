package llm

import (
	"fmt"
	"math"
	"strings"
)

func defaultOutputTokens(totalContextTokens int) int {
	if totalContextTokens <= 0 {
		return 4000
	}

	outputTokens := totalContextTokens / 8
	if outputTokens < 4000 {
		outputTokens = 4000
	}
	if outputTokens > maximumDefaultOutputTokens {
		outputTokens = maximumDefaultOutputTokens
	}

	return outputTokens
}

func calculateTokenBudgets(totalContextTokens, configuredMaxOutputTokens int) (int, int) {
	if totalContextTokens <= 0 {
		return 6000, 4000
	}

	maxOutputTokens := configuredMaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = defaultOutputTokens(totalContextTokens)
	}

	maxAllowedOutputTokens := totalContextTokens - defaultPromptReserveTokens - minimumChunkInputTokens
	if maxAllowedOutputTokens < minimumOutputTokens {
		maxAllowedOutputTokens = totalContextTokens / 2
		if maxAllowedOutputTokens < minimumOutputTokens {
			maxAllowedOutputTokens = minimumOutputTokens
		}
	}

	if maxOutputTokens > maxAllowedOutputTokens {
		maxOutputTokens = maxAllowedOutputTokens
	}
	if maxOutputTokens < minimumOutputTokens {
		maxOutputTokens = minimumOutputTokens
	}

	maxInputTokens := totalContextTokens - maxOutputTokens - defaultPromptReserveTokens
	if maxInputTokens < minimumChunkInputTokens {
		maxInputTokens = totalContextTokens - maxOutputTokens - defaultPromptReserveTokens
		if maxInputTokens < minimumOutputTokens {
			maxInputTokens = minimumOutputTokens
		}
	}

	return maxInputTokens, maxOutputTokens
}

// estimateTokens 估算文本的 token 数量
func estimateTokens(text string) int {
	// 简单估算：中文约 1.5 token/字，英文约 1.3 token/词
	// 再叠加一个安全系数，让本地预算比启发式估算更保守一些。
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
	if tokens == 0 {
		return 0
	}

	return int(math.Ceil(float64(tokens) * tokenEstimateSafetyFactor))
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
