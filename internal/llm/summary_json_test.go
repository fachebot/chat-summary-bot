package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractJSONPayload(t *testing.T) {
	raw := "一些前置说明\n{\"topics\":[{\"title\":\"A\",\"items\":[{\"sender_name\":\"X\",\"description\":\"d\",\"message_ids\":[1]}]}]}, trailing"
	assert.Equal(t, `{"topics":[{"title":"A","items":[{"sender_name":"X","description":"d","message_ids":[1]}]}]}`, extractJSONPayload(raw))
}

func TestParseTopicsSummary_RepairsFlattenedTopics(t *testing.T) {
	msgs := []ChatMessage{
		{MessageID: 100, SenderID: 1, SenderName: "A", Text: "foo"},
		{MessageID: 200, SenderID: 2, SenderName: "B", Text: "bar"},
	}
	allowedMessageIDs, allowedSenders := buildAllowedSetsFromMessages(msgs)
	raw := `{
		"topics": [
			{
				"topic": "话题一",
				"sender_name": "A",
				"message_ids": [100],
				"description": "A 的总结"
			},
			{
				"topic": "话题二",
				"sender_name": "B",
				"message_ids": [200],
				"description": "B 的总结"
			}
		]
	}`

	summary, err := parseTopicsSummary(raw, allowedMessageIDs, allowedSenders)
	assert.NoError(t, err)
	assert.Len(t, summary.Topics, 2)
	assert.Equal(t, "话题一", summary.Topics[0].Title)
	assert.Equal(t, "A", summary.Topics[0].Items[0].SenderName)
	assert.Equal(t, []int64{100}, summary.Topics[0].Items[0].MessageIDs)
}

func TestParseTopicsSummary_RepairsStringMessageIDsAndMissingTitle(t *testing.T) {
	msgs := []ChatMessage{
		{MessageID: 100, SenderID: 1, SenderName: "A", Text: "foo"},
	}
	allowedMessageIDs, allowedSenders := buildAllowedSetsFromMessages(msgs)
	raw := `{
		"topics": [
			{
				"sender_name": "A",
				"message_ids": ["100"],
				"description": "A 发布了重要更新。后续还有补充。"
			}
		]
	}`

	summary, err := parseTopicsSummary(raw, allowedMessageIDs, allowedSenders)
	assert.NoError(t, err)
	assert.Len(t, summary.Topics, 1)
	assert.NotEmpty(t, summary.Topics[0].Title)
	assert.Equal(t, []int64{100}, summary.Topics[0].Items[0].MessageIDs)
	assert.Equal(t, "A", summary.Topics[0].Items[0].SenderName)
}
