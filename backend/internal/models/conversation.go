package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type ChatTurn struct {
	Role      string    `bson:"role" json:"role"`
	Text      string    `bson:"text" json:"text"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

type Conversation struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    primitive.ObjectID `bson:"user_id" json:"-"` // non exposé
	Title     string             `bson:"title" json:"title"`
	Messages  []ChatTurn         `bson:"messages" json:"messages"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
}

type ConversationListItem struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Title     string             `bson:"title" json:"title"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
}
