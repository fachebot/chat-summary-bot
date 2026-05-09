package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// Message holds the schema definition for the Message entity.
type Message struct {
	ent.Schema
}

func (Message) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}

// Fields of the Message.
func (Message) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("message_id").Comment("Telegram消息ID"),
		field.Int64("chat_id").Comment("群聊ID"),
		field.Int64("sender_id").Comment("发送者用户ID"),
		field.String("sender_name").Comment("发送者名称"),
		field.String("sender_username").Optional().Comment("发送者用户名，如 @zhangsan"),
		field.Text("text").Comment("消息文本内容"),
		field.Time("sent_at").Comment("消息发送时间"),
	}
}

func (Message) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chat_id", "message_id").Unique(),
	}
}
