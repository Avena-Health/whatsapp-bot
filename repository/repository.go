package repository

import (
	"context"
	"time"
)

// MessageDoc represents a WhatsApp message to store in MongoDB
type MessageDoc struct {
	WhatsAppID string    `bson:"whatsappId"`
	Text       string    `bson:"text"`
	Phone      string    `bson:"phone"`
	Timestamp  time.Time `bson:"timestamp"`
	GroupName  string    `bson:"groupName"`
}

// RecordDoc represents a group join record for MongoDB
type RecordDoc struct {
	Phone     string `bson:"phone"`
	Lada      string `bson:"lada,omitempty"`
	GroupName string `bson:"groupName"`
}

// MessageRepository saves and queries messages in MongoDB
type MessageRepository interface {
	SaveMessage(ctx context.Context, msg *MessageDoc) error
	SaveRecord(ctx context.Context, record *RecordDoc) error
	FindMessages(ctx context.Context, start, end *time.Time) ([]*MessageDoc, error)
}
