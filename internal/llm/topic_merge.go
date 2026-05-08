package llm

import (
	"fmt"
	"strings"
	"unicode"
)

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

func normalizeTopicTitle(title string) string {
	var sb strings.Builder
	for _, r := range strings.TrimSpace(strings.ToLower(title)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func runeJaccardSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}

	aSet := make(map[rune]struct{})
	bSet := make(map[rune]struct{})
	for _, r := range a {
		aSet[r] = struct{}{}
	}
	for _, r := range b {
		bSet[r] = struct{}{}
	}

	intersection := 0
	union := len(aSet)
	for r := range bSet {
		if _, exists := aSet[r]; exists {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func senderOverlapScore(a, b topicItemJSON) float64 {
	if len(a.Items) == 0 || len(b.Items) == 0 {
		return 0
	}

	aSet := make(map[string]struct{}, len(a.Items))
	for _, item := range a.Items {
		aSet[item.SenderName] = struct{}{}
	}

	overlap := 0
	for _, item := range b.Items {
		if _, exists := aSet[item.SenderName]; exists {
			overlap++
		}
	}
	if overlap == 0 {
		return 0
	}

	minSize := len(a.Items)
	if len(b.Items) < minSize {
		minSize = len(b.Items)
	}
	if minSize == 0 {
		return 0
	}
	return float64(overlap) / float64(minSize)
}

func topicMatchScore(a, b topicItemJSON) float64 {
	left := normalizeTopicTitle(a.Title)
	right := normalizeTopicTitle(b.Title)
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	if strings.Contains(left, right) || strings.Contains(right, left) {
		return 0.95
	}

	titleScore := runeJaccardSimilarity(left, right)
	senderScore := senderOverlapScore(a, b)
	return titleScore*0.75 + senderScore*0.25
}

func findMatchingTopicIndex(topics []topicItemJSON, target topicItemJSON) int {
	targetNormalized := normalizeTopicTitle(target.Title)
	for i, topic := range topics {
		if normalizeTopicTitle(topic.Title) == targetNormalized {
			return i
		}
	}

	bestIndex := -1
	bestScore := 0.0
	for i, topic := range topics {
		score := topicMatchScore(topic, target)
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	if bestScore >= minimumTopicMatchScore {
		return bestIndex
	}
	return -1
}

func mergeTopicTitle(oldTitle, newTitle string) string {
	oldTitle = strings.TrimSpace(oldTitle)
	newTitle = strings.TrimSpace(newTitle)
	if oldTitle == "" {
		return newTitle
	}
	if newTitle == "" {
		return oldTitle
	}

	oldNormalized := normalizeTopicTitle(oldTitle)
	newNormalized := normalizeTopicTitle(newTitle)
	if oldNormalized == newNormalized {
		if len([]rune(newTitle)) > len([]rune(oldTitle)) {
			return newTitle
		}
		return oldTitle
	}
	if strings.Contains(newNormalized, oldNormalized) && len(newNormalized) > len(oldNormalized) {
		return newTitle
	}
	return oldTitle
}

func mergeDescriptions(oldDescription, newDescription string) string {
	oldDescription = strings.TrimSpace(oldDescription)
	newDescription = strings.TrimSpace(newDescription)
	if oldDescription == "" {
		return newDescription
	}
	if newDescription == "" {
		return oldDescription
	}
	if strings.Contains(oldDescription, newDescription) {
		return oldDescription
	}
	if strings.Contains(newDescription, oldDescription) {
		return newDescription
	}
	return oldDescription + "；" + newDescription
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

	result := &topicsSummaryJSON{
		Topics: make([]topicItemJSON, len(accumulated.Topics)),
	}
	copy(result.Topics, accumulated.Topics)

	for _, pt := range partial.Topics {
		if oldIdx := findMatchingTopicIndex(result.Topics, pt); oldIdx >= 0 {
			result.Topics[oldIdx] = mergeTopicItems(result.Topics[oldIdx], pt)
		} else {
			result.Topics = append(result.Topics, pt)
		}
	}

	return result
}

// mergeTopicItems 合并同一话题下的 items，按 sender_name 去重并合并 message_ids
func mergeTopicItems(old, new topicItemJSON) topicItemJSON {
	merged := topicItemJSON{
		Title: mergeTopicTitle(old.Title, new.Title),
		Items: make([]topicSubItemJSON, 0),
	}

	oldItemMap := make(map[string]int)
	for i, item := range old.Items {
		oldItemMap[item.SenderName] = i
	}

	merged.Items = append(merged.Items, old.Items...)

	for _, newItem := range new.Items {
		if oldIdx, exists := oldItemMap[newItem.SenderName]; exists {
			mergedIDs := mergeMessageIDs(merged.Items[oldIdx].MessageIDs, newItem.MessageIDs)
			merged.Items[oldIdx] = topicSubItemJSON{
				SenderName:  newItem.SenderName,
				Description: mergeDescriptions(merged.Items[oldIdx].Description, newItem.Description),
				MessageIDs:  mergedIDs,
			}
		} else {
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

func splitSummaryBatchesForMerge(summaries []*topicsSummaryJSON, maxTokensPerBatch int) [][]*topicsSummaryJSON {
	if len(summaries) == 0 {
		return nil
	}
	if maxTokensPerBatch < minimumMergeBatchTokens {
		maxTokensPerBatch = minimumMergeBatchTokens
	}

	batches := make([][]*topicsSummaryJSON, 0)
	current := make([]*topicsSummaryJSON, 0)
	currentTokens := 0

	for _, summary := range summaries {
		tokens := estimateTokens(formatSummaryBatchForPrompt([]*topicsSummaryJSON{summary}))
		if currentTokens+tokens > maxTokensPerBatch && len(current) > 0 {
			batches = append(batches, current)
			current = nil
			currentTokens = 0
		}
		current = append(current, summary)
		currentTokens += tokens
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func mergeSummaryBatchFallback(batch []*topicsSummaryJSON) *topicsSummaryJSON {
	var merged *topicsSummaryJSON
	for _, summary := range batch {
		merged = mergeTopics(merged, summary)
	}
	return merged
}
