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

// PersonalityTrait 性格分析中的一项特征，包含标签和详细说明
type PersonalityTrait struct {
	Trait       string `json:"trait"`       // 特征标签，如"极度理性与思辨型"
	Explanation string `json:"explanation"` // 详细说明，结合实际聊天内容佐证
}

// PersonalityProfile 性格分析结构化输出
type PersonalityProfile struct {
	Summary            string             `json:"summary"`
	PersonalityTraits  []PersonalityTrait `json:"personality_traits"`
	CommunicationStyle []PersonalityTrait `json:"communication_style"`
	Interests          []PersonalityTrait `json:"interests"`
	BehaviorPatterns   []PersonalityTrait `json:"behavior_patterns"`
	OverallAssessment  string             `json:"overall_assessment"`
}
