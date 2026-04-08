package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Sock5Proxy struct {
	Host   string `yaml:"Host"`
	Port   int32  `yaml:"Port"`
	Enable bool   `yaml:"Enable"`
}

type TelegramApp struct {
	ApiId   int32  `yaml:"ApiId"`
	ApiHash string `yaml:"ApiHash"`
}

type LLM struct {
	BaseURL   string `yaml:"BaseURL"` // 兼容 OpenAI API 的端点
	APIKey    string `yaml:"APIKey"`
	Model     string `yaml:"Model"`     // 如 gpt-4o, deepseek-chat, qwen-plus
	MaxTokens int    `yaml:"MaxTokens"` // 模型上下文窗口大小
}

type Summary struct {
	Cron          string  `yaml:"Cron"`          // cron 表达式，如 "0 23 * * *"
	RetentionDays int     `yaml:"RetentionDays"` // 消息保留天数
	RangeDays     int     `yaml:"RangeDays"`     // 总结天数，1=仅昨天，7=最近7天
	NotifyMode    string  `yaml:"NotifyMode"`    // "private" / "group" / "both"
	NotifyUserIds []int64 `yaml:"NotifyUserIds"` // 私聊通知的目标用户ID列表
	RetryTimes    int     `yaml:"RetryTimes"`    // 总结失败重试次数，默认 3
	RetryInterval int     `yaml:"RetryInterval"` // 重试间隔（秒），默认 60
	Whitelist     []int64 `yaml:"Whitelist"`     // 白名单群组ID列表，设置后只保存和总结白名单群组
	Blacklist     []int64 `yaml:"Blacklist"`     // 黑名单群组ID列表，设置后不保存和总结黑名单群组
	AdminUserIds  []int64 `yaml:"AdminUserIds"`  // 手动触发摘要的白名单用户ID列表
}

type MarketIndicator struct {
	Enable    bool    `yaml:"Enable"`    // 是否启用指标广播
	Cron      string  `yaml:"Cron"`      // cron 表达式，如 "0 1 * * *"
	Whitelist []int64 `yaml:"Whitelist"` // 白名单群组ID列表
	Blacklist []int64 `yaml:"Blacklist"` // 黑名单群组ID列表
}

type Config struct {
	Sock5Proxy      Sock5Proxy      `yaml:"Sock5Proxy"`
	TelegramApp     TelegramApp     `yaml:"TelegramApp"`
	LLM             LLM             `yaml:"LLM"`
	Summary         Summary         `yaml:"Summary"`
	MarketIndicator MarketIndicator `yaml:"MarketIndicator"`
}

func LoadFromFile(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var c Config
	err = yaml.Unmarshal([]byte(data), &c)
	if err != nil {
		return nil, err
	}

	// 验证配置
	if err := c.Validate(); err != nil {
		return nil, err
	}

	return &c, nil
}

// Validate 验证配置的有效性
func (c *Config) Validate() error {
	// 验证 TelegramApp
	if c.TelegramApp.ApiId == 0 {
		return fmt.Errorf("TelegramApp.ApiId 不能为空")
	}
	if c.TelegramApp.ApiHash == "" {
		return fmt.Errorf("TelegramApp.ApiHash 不能为空")
	}

	// 验证 LLM
	if c.LLM.APIKey == "" {
		return fmt.Errorf("LLM.APIKey 不能为空")
	}
	if c.LLM.BaseURL == "" {
		return fmt.Errorf("LLM.BaseURL 不能为空")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("LLM.Model 不能为空")
	}
	if c.LLM.MaxTokens <= 0 {
		return fmt.Errorf("LLM.MaxTokens 必须大于 0")
	}

	// 验证 Summary
	if c.Summary.Cron == "" {
		return fmt.Errorf("Summary.Cron 不能为空")
	}
	if c.Summary.RetentionDays < 0 {
		return fmt.Errorf("Summary.RetentionDays 必须 >= 0")
	}
	if c.Summary.RangeDays < 0 {
		return fmt.Errorf("Summary.RangeDays 必须 >= 0")
	}
	if c.Summary.RetryTimes < 0 {
		return fmt.Errorf("Summary.RetryTimes 必须 >= 0")
	}
	if c.Summary.RetryInterval < 0 {
		return fmt.Errorf("Summary.RetryInterval 必须 >= 0")
	}
	if c.Summary.NotifyMode != "private" && c.Summary.NotifyMode != "group" && c.Summary.NotifyMode != "both" {
		return fmt.Errorf("Summary.NotifyMode 必须是 'private', 'group' 或 'both'")
	}
	if c.Summary.NotifyMode == "private" || c.Summary.NotifyMode == "both" {
		if len(c.Summary.NotifyUserIds) == 0 {
			return fmt.Errorf("Summary.NotifyUserIds 不能为空（当 NotifyMode 为 'private' 或 'both' 时）")
		}
	}
	if len(c.Summary.Whitelist) > 0 && len(c.Summary.Blacklist) > 0 {
		return fmt.Errorf("Whitelist 和 Blacklist 不能同时设置")
	}

	return nil
}

// FilterChatIDs 根据白名单/黑名单过滤群组ID
func (s *Summary) FilterChatIDs(chatIDs []int64) []int64 {
	whitelist := s.Whitelist
	blacklist := s.Blacklist

	// 白名单优先
	if len(whitelist) > 0 {
		filtered := make([]int64, 0)
		for _, id := range chatIDs {
			for _, wid := range whitelist {
				if id == wid {
					filtered = append(filtered, id)
					break
				}
			}
		}
		return filtered
	}

	// 黑名单过滤
	if len(blacklist) > 0 {
		filtered := make([]int64, 0)
		for _, id := range chatIDs {
			blocked := false
			for _, bid := range blacklist {
				if id == bid {
					blocked = true
					break
				}
			}
			if !blocked {
				filtered = append(filtered, id)
			}
		}
		return filtered
	}

	return chatIDs
}

// ShouldSaveMessage 判断是否应该保存该群组的消息
func (s *Summary) ShouldSaveMessage(chatID int64) bool {
	whitelist := s.Whitelist
	blacklist := s.Blacklist

	// 白名单优先
	if len(whitelist) > 0 {
		for _, id := range whitelist {
			if id == chatID {
				return true
			}
		}
		return false
	}

	// 黑名单检查
	if len(blacklist) > 0 {
		for _, id := range blacklist {
			if id == chatID {
				return false
			}
		}
	}

	return true
}

// FilterChatIDs 根据白名单/黑名单过滤群组ID
func (m *MarketIndicator) FilterChatIDs(chatIDs []int64) []int64 {
	whitelist := m.Whitelist
	blacklist := m.Blacklist

	if len(whitelist) > 0 {
		filtered := make([]int64, 0)
		for _, id := range chatIDs {
			for _, wid := range whitelist {
				if id == wid {
					filtered = append(filtered, id)
					break
				}
			}
		}
		return filtered
	}

	if len(blacklist) > 0 {
		filtered := make([]int64, 0)
		for _, id := range chatIDs {
			blocked := false
			for _, bid := range blacklist {
				if id == bid {
					blocked = true
					break
				}
			}
			if !blocked {
				filtered = append(filtered, id)
			}
		}
		return filtered
	}

	return chatIDs
}
