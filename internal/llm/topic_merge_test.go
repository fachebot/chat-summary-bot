package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatTopicsForContext(t *testing.T) {
	topics := []topicItemJSON{
		{
			Title: "话题A",
			Items: []topicSubItemJSON{
				{SenderName: "张三", Description: "说了什么", MessageIDs: []int64{100, 101}},
			},
		},
	}
	got := formatTopicsForContext(topics)
	assert.Contains(t, got, "1. 话题A")
	assert.Contains(t, got, "张三")
	assert.Contains(t, got, "100,101")
}

func TestMergeTopics(t *testing.T) {
	t.Run("accumulated 为 nil", func(t *testing.T) {
		partial := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "A", Items: []topicSubItemJSON{{SenderName: "X", Description: "d1", MessageIDs: []int64{1}}}},
			},
		}
		result := mergeTopics(nil, partial)
		assert.Len(t, result.Topics, 1)
		assert.Equal(t, "A", result.Topics[0].Title)
	})

	t.Run("同名话题合并", func(t *testing.T) {
		accumulated := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "A", Items: []topicSubItemJSON{{SenderName: "X", Description: "old desc", MessageIDs: []int64{1, 2}}}},
			},
		}
		partial := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "A", Items: []topicSubItemJSON{
					{SenderName: "X", Description: "new desc", MessageIDs: []int64{2, 3}},
					{SenderName: "Y", Description: "y desc", MessageIDs: []int64{4}},
				}},
			},
		}
		result := mergeTopics(accumulated, partial)
		assert.Len(t, result.Topics, 1)
		xItem := result.Topics[0].Items[0]
		assert.Equal(t, "X", xItem.SenderName)
		assert.Equal(t, "old desc；new desc", xItem.Description)
		assert.ElementsMatch(t, []int64{1, 2, 3}, xItem.MessageIDs)
		assert.Len(t, result.Topics[0].Items, 2)
		assert.Equal(t, "Y", result.Topics[0].Items[1].SenderName)
	})

	t.Run("近似标题也会合并", func(t *testing.T) {
		accumulated := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "BTC走势讨论", Items: []topicSubItemJSON{{SenderName: "X", Description: "old", MessageIDs: []int64{1}}}},
			},
		}
		partial := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "BTC 走势", Items: []topicSubItemJSON{{SenderName: "X", Description: "new", MessageIDs: []int64{2}}}},
			},
		}

		result := mergeTopics(accumulated, partial)
		assert.Len(t, result.Topics, 1)
		assert.Equal(t, []int64{1, 2}, result.Topics[0].Items[0].MessageIDs)
	})

	t.Run("新话题追加", func(t *testing.T) {
		accumulated := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "A", Items: []topicSubItemJSON{{SenderName: "X", Description: "d1", MessageIDs: []int64{1}}}},
			},
		}
		partial := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "B", Items: []topicSubItemJSON{{SenderName: "Y", Description: "d2", MessageIDs: []int64{2}}}},
			},
		}
		result := mergeTopics(accumulated, partial)
		assert.Len(t, result.Topics, 2)
		assert.Equal(t, "A", result.Topics[0].Title)
		assert.Equal(t, "B", result.Topics[1].Title)
	})

	t.Run("旧话题保留", func(t *testing.T) {
		accumulated := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "A", Items: []topicSubItemJSON{{SenderName: "X", Description: "d1", MessageIDs: []int64{1}}}},
				{Title: "B", Items: []topicSubItemJSON{{SenderName: "Y", Description: "d2", MessageIDs: []int64{2}}}},
			},
		}
		partial := &topicsSummaryJSON{
			Topics: []topicItemJSON{
				{Title: "A", Items: []topicSubItemJSON{{SenderName: "X", Description: "updated", MessageIDs: []int64{1, 3}}}},
			},
		}
		result := mergeTopics(accumulated, partial)
		assert.Len(t, result.Topics, 2)
		assert.Equal(t, "B", result.Topics[1].Title)
	})
}

func TestMergeMessageIDs(t *testing.T) {
	result := mergeMessageIDs([]int64{1, 2, 3}, []int64{2, 3, 4})
	assert.ElementsMatch(t, []int64{1, 2, 3, 4}, result)

	result = mergeMessageIDs(nil, []int64{1, 2})
	assert.ElementsMatch(t, []int64{1, 2}, result)

	result = mergeMessageIDs([]int64{1, 2}, nil)
	assert.ElementsMatch(t, []int64{1, 2}, result)
}
