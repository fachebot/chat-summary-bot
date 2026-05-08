package llm

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
	Title string             `json:"title"`
	Items []topicSubItemJSON `json:"items"`
}

type topicSubItemJSON struct {
	SenderName  string  `json:"sender_name"`
	Description string  `json:"description"`
	MessageIDs  []int64 `json:"message_ids"`
}
