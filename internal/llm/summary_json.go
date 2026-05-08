package llm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func formatSummaryBatchForPrompt(summaries []*topicsSummaryJSON) string {
	var sb strings.Builder
	for i, summary := range summaries {
		data, err := json.Marshal(summary)
		if err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("分块摘要 %d:\n%s\n\n", i+1, string(data)))
	}
	return strings.TrimSpace(sb.String())
}

func trimResponseContent(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	replacer := strings.NewReplacer("\ufeff", "", "\u200b", "", "\u200c", "", "\u200d", "")
	return strings.TrimSpace(replacer.Replace(content))
}

func extractJSONPayload(content string) string {
	content = trimResponseContent(content)
	if content == "" {
		return content
	}
	if (strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}")) || (strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]")) {
		return content
	}

	start := -1
	stack := make([]rune, 0)
	inString := false
	escaped := false

	for i, r := range content {
		if start == -1 {
			if r == '{' || r == '[' {
				start = i
				if r == '{' {
					stack = append(stack, '}')
				} else {
					stack = append(stack, ']')
				}
			}
			continue
		}

		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}

		switch r {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 {
				continue
			}
			expected := stack[len(stack)-1]
			if r != expected {
				continue
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return strings.TrimSpace(content[start : i+utf8.RuneLen(r)])
			}
		}
	}

	if start >= 0 {
		return strings.TrimSpace(content[start:])
	}

	return content
}

func decodeJSONValue(content string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()

	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func coerceString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	default:
		return ""
	}
}

func firstNonEmptyString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, exists := record[key]; exists {
			if text := coerceString(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func coerceInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		if id, err := v.Int64(); err == nil {
			return id, true
		}
		if number, err := v.Float64(); err == nil {
			return int64(number), true
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		if id, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return id, true
		}
		if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int64(number), true
		}
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case uint64:
		return int64(v), true
	case uint32:
		return int64(v), true
	}

	return 0, false
}

func coerceMessageIDs(value any) []int64 {
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})

	appendID := func(id int64) {
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if id, ok := coerceInt64(item); ok {
				appendID(id)
			}
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return ids
		}
		if strings.HasPrefix(trimmed, "[") {
			if nested, err := decodeJSONValue(trimmed); err == nil {
				for _, id := range coerceMessageIDs(nested) {
					appendID(id)
				}
				return ids
			}
		}
		parts := strings.FieldsFunc(trimmed, func(r rune) bool {
			return r == ',' || r == '，' || r == ';' || r == '；' || unicode.IsSpace(r)
		})
		for _, part := range parts {
			if id, ok := coerceInt64(part); ok {
				appendID(id)
			}
		}
	default:
		if id, ok := coerceInt64(v); ok {
			appendID(id)
		}
	}

	return ids
}

func normalizeSenderKey(sender string) string {
	var sb strings.Builder
	for _, r := range strings.TrimSpace(strings.ToLower(sender)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func resolveAllowedSenderName(sender string, allowedSenders map[string]struct{}) (string, bool) {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return "", false
	}
	if len(allowedSenders) == 0 {
		return sender, true
	}
	if _, exists := allowedSenders[sender]; exists {
		return sender, true
	}

	normalized := normalizeSenderKey(sender)
	if normalized == "" {
		return "", false
	}

	matched := ""
	for allowedSender := range allowedSenders {
		if normalizeSenderKey(allowedSender) == normalized {
			if matched != "" {
				matched = ""
				break
			}
			matched = allowedSender
		}
	}
	if matched != "" {
		return matched, true
	}

	bestSender := ""
	bestScore := 0.0
	secondScore := 0.0
	for allowedSender := range allowedSenders {
		allowedKey := normalizeSenderKey(allowedSender)
		score := runeJaccardSimilarity(normalized, allowedKey)
		if strings.Contains(allowedKey, normalized) || strings.Contains(normalized, allowedKey) {
			if score < 0.92 {
				score = 0.92
			}
		}
		if score > bestScore {
			secondScore = bestScore
			bestScore = score
			bestSender = allowedSender
		} else if score > secondScore {
			secondScore = score
		}
	}
	if bestScore >= 0.88 && bestScore-secondScore >= 0.05 {
		return bestSender, true
	}

	return "", false
}

func truncateRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return strings.TrimSpace(text)
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func inferTopicTitle(record map[string]any, fallback string) string {
	title := firstNonEmptyString(record, "title", "topic", "theme", "subject", "name", "label")
	if title != "" {
		return title
	}

	description := firstNonEmptyString(record, "description", "summary", "desc", "content", "text")
	sentences := strings.FieldsFunc(description, func(r rune) bool {
		return r == '\n' || r == '。' || r == '；' || r == ';' || r == '!' || r == '！' || r == '?' || r == '？'
	})
	if len(sentences) > 0 {
		title = strings.TrimSpace(sentences[0])
	}
	if title == "" {
		title = fallback
	}
	if title == "" {
		title = firstNonEmptyString(record, "sender_name", "sender")
		if title != "" {
			title += "相关内容"
		}
	}
	return truncateRunes(title, 24)
}

func coerceSubItemFromMap(record map[string]any, allowedSenders map[string]struct{}) (topicSubItemJSON, bool) {
	rawSender := firstNonEmptyString(record, "sender_name", "sender", "speaker", "author")
	senderName, ok := resolveAllowedSenderName(rawSender, allowedSenders)
	if !ok {
		return topicSubItemJSON{}, false
	}

	description := firstNonEmptyString(record, "description", "summary", "desc", "content", "text")
	if description == "" {
		return topicSubItemJSON{}, false
	}

	messageIDs := coerceMessageIDs(record["message_ids"])
	if len(messageIDs) == 0 {
		messageIDs = coerceMessageIDs(record["messageIds"])
	}
	if len(messageIDs) == 0 {
		messageIDs = coerceMessageIDs(record["msg_ids"])
	}
	if len(messageIDs) == 0 {
		messageIDs = coerceMessageIDs(record["ids"])
	}
	if len(messageIDs) == 0 {
		return topicSubItemJSON{}, false
	}

	return topicSubItemJSON{
		SenderName:  senderName,
		Description: strings.TrimSpace(description),
		MessageIDs:  messageIDs,
	}, true
}

func coerceSubItems(value any, allowedSenders map[string]struct{}) []topicSubItemJSON {
	items := make([]topicSubItemJSON, 0)
	appendItem := func(item topicSubItemJSON) {
		items = append(items, item)
	}

	switch v := value.(type) {
	case []any:
		for _, rawItem := range v {
			if record, ok := rawItem.(map[string]any); ok {
				if item, ok := coerceSubItemFromMap(record, allowedSenders); ok {
					appendItem(item)
				}
			}
		}
	case map[string]any:
		if item, ok := coerceSubItemFromMap(v, allowedSenders); ok {
			appendItem(item)
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if decoded, err := decodeJSONValue(trimmed); err == nil {
				return coerceSubItems(decoded, allowedSenders)
			}
		}
	}

	return items
}

func coerceTopicFromMap(record map[string]any, fallbackTitle string, allowedSenders map[string]struct{}) (topicItemJSON, bool) {
	title := inferTopicTitle(record, fallbackTitle)
	itemsValue, exists := record["items"]
	if !exists {
		itemsValue, exists = record["entries"]
	}
	if !exists {
		itemsValue, exists = record["records"]
	}
	if !exists {
		return topicItemJSON{}, false
	}

	items := coerceSubItems(itemsValue, allowedSenders)
	if title == "" || len(items) == 0 {
		return topicItemJSON{}, false
	}

	return topicItemJSON{Title: title, Items: items}, true
}

func coerceFlatTopicRecord(record map[string]any, fallbackTitle string, allowedSenders map[string]struct{}) (topicItemJSON, bool) {
	if _, hasSender := record["sender_name"]; !hasSender {
		if _, hasSender = record["sender"]; !hasSender {
			return topicItemJSON{}, false
		}
	}

	item, ok := coerceSubItemFromMap(record, allowedSenders)
	if !ok {
		return topicItemJSON{}, false
	}

	title := inferTopicTitle(record, fallbackTitle)
	if title == "" {
		return topicItemJSON{}, false
	}

	return topicItemJSON{Title: title, Items: []topicSubItemJSON{item}}, true
}

func repairTopicsSummary(content string, allowedMessageIDs map[int64]struct{}, allowedSenders map[string]struct{}) (*topicsSummaryJSON, error) {
	payload, err := decodeJSONValue(content)
	if err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	var summary *topicsSummaryJSON
	var collect func(any, string)
	collect = func(node any, fallbackTitle string) {
		switch value := node.(type) {
		case map[string]any:
			for _, key := range []string{"topics", "data", "result", "output", "response", "json", "content"} {
				if child, exists := value[key]; exists {
					beforeCount := 0
					if summary != nil {
						beforeCount = len(summary.Topics)
					}
					collect(child, fallbackTitle)
					if summary != nil && len(summary.Topics) > beforeCount {
						return
					}
				}
			}

			if topic, ok := coerceTopicFromMap(value, fallbackTitle, allowedSenders); ok {
				summary = mergeTopics(summary, &topicsSummaryJSON{Topics: []topicItemJSON{topic}})
				return
			}
			if topic, ok := coerceFlatTopicRecord(value, fallbackTitle, allowedSenders); ok {
				summary = mergeTopics(summary, &topicsSummaryJSON{Topics: []topicItemJSON{topic}})
				return
			}

			for key, child := range value {
				switch key {
				case "title", "topic", "items", "sender_name", "sender", "description", "summary", "message_ids", "messageIds", "ids":
					continue
				default:
					collect(child, fallbackTitle)
				}
			}
		case []any:
			for i, child := range value {
				childFallback := fallbackTitle
				if childFallback == "" {
					childFallback = fmt.Sprintf("话题%d", i+1)
				}
				collect(child, childFallback)
			}
		case string:
			trimmed := strings.TrimSpace(value)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				if decoded, err := decodeJSONValue(trimmed); err == nil {
					collect(decoded, fallbackTitle)
				}
			}
		}
	}

	collect(payload, "")
	if summary == nil {
		return nil, fmt.Errorf("topics 为空或全部无效")
	}
	return normalizeAndValidateTopicsSummary(summary, allowedMessageIDs, allowedSenders)
}

func buildAllowedSetsFromMessages(msgs []ChatMessage) (map[int64]struct{}, map[string]struct{}) {
	allowedMessageIDs := make(map[int64]struct{}, len(msgs))
	allowedSenders := make(map[string]struct{}, len(msgs))
	for _, msg := range msgs {
		allowedMessageIDs[msg.MessageID] = struct{}{}
		allowedSenders[msg.SenderName] = struct{}{}
	}
	return allowedMessageIDs, allowedSenders
}

func buildAllowedSetsFromSummaries(summaries []*topicsSummaryJSON) (map[int64]struct{}, map[string]struct{}) {
	allowedMessageIDs := make(map[int64]struct{})
	allowedSenders := make(map[string]struct{})
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		for _, topic := range summary.Topics {
			for _, item := range topic.Items {
				allowedSenders[item.SenderName] = struct{}{}
				for _, messageID := range item.MessageIDs {
					allowedMessageIDs[messageID] = struct{}{}
				}
			}
		}
	}
	return allowedMessageIDs, allowedSenders
}

func normalizeAndValidateTopicsSummary(summary *topicsSummaryJSON, allowedMessageIDs map[int64]struct{}, allowedSenders map[string]struct{}) (*topicsSummaryJSON, error) {
	if summary == nil {
		return nil, fmt.Errorf("topics 为空")
	}

	var normalized *topicsSummaryJSON
	for _, topic := range summary.Topics {
		normalizedTopic := topicItemJSON{
			Title: strings.TrimSpace(topic.Title),
			Items: make([]topicSubItemJSON, 0, len(topic.Items)),
		}
		if normalizedTopic.Title == "" {
			continue
		}

		for _, item := range topic.Items {
			senderName := strings.TrimSpace(item.SenderName)
			description := strings.TrimSpace(item.Description)
			if senderName == "" || description == "" {
				continue
			}
			if len(allowedSenders) > 0 {
				if _, exists := allowedSenders[senderName]; !exists {
					continue
				}
			}

			messageIDs := make([]int64, 0, len(item.MessageIDs))
			seenMessageIDs := make(map[int64]struct{}, len(item.MessageIDs))
			for _, messageID := range item.MessageIDs {
				if _, exists := seenMessageIDs[messageID]; exists {
					continue
				}
				if len(allowedMessageIDs) > 0 {
					if _, exists := allowedMessageIDs[messageID]; !exists {
						continue
					}
				}
				seenMessageIDs[messageID] = struct{}{}
				messageIDs = append(messageIDs, messageID)
			}
			if len(messageIDs) == 0 {
				continue
			}

			normalizedTopic.Items = append(normalizedTopic.Items, topicSubItemJSON{
				SenderName:  senderName,
				Description: description,
				MessageIDs:  messageIDs,
			})
		}

		if len(normalizedTopic.Items) == 0 {
			continue
		}
		normalized = mergeTopics(normalized, &topicsSummaryJSON{Topics: []topicItemJSON{normalizedTopic}})
	}

	if normalized == nil || len(normalized.Topics) == 0 {
		return nil, fmt.Errorf("topics 为空或全部无效")
	}
	return normalized, nil
}

func parseTopicsSummary(raw string, allowedMessageIDs map[int64]struct{}, allowedSenders map[string]struct{}) (*topicsSummaryJSON, error) {
	content := extractJSONPayload(raw)

	var parsed topicsSummaryJSON
	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		if normalized, normalizeErr := normalizeAndValidateTopicsSummary(&parsed, allowedMessageIDs, allowedSenders); normalizeErr == nil {
			return normalized, nil
		}
	}

	return repairTopicsSummary(content, allowedMessageIDs, allowedSenders)
}
