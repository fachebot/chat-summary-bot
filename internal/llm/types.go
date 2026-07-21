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

// PersonalityProfile 性格分析结构化输出
type PersonalityProfile struct {
	Summary            string   `json:"summary"`
	PersonalityTraits  []string `json:"personality_traits"`
	CommunicationStyle []string `json:"communication_style"`
	Interests          []string `json:"interests"`
	BehaviorPatterns   []string `json:"behavior_patterns"`
	OverallAssessment  string   `json:"overall_assessment"`
}
