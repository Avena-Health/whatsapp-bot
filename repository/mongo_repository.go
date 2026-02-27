package repository

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	messagesCollection = "messages"
	recordsCollection  = "records"
)

// MongoRepository implements MessageRepository using MongoDB
type MongoRepository struct {
	messages *mongo.Collection
	records  *mongo.Collection
}

// NewMongoRepository connects to MongoDB and returns a repository
func NewMongoRepository(ctx context.Context, uri, dbName string) (*MongoRepository, error) {
	if dbName == "" {
		dbName = "whatsbot"
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}
	log.Printf("✅ MongoDB connected successfully")
	db := client.Database(dbName)
	return &MongoRepository{
		messages: db.Collection(messagesCollection),
		records:  db.Collection(recordsCollection),
	}, nil
}

// SaveMessage inserts a message document
func (r *MongoRepository) SaveMessage(ctx context.Context, msg *MessageDoc) error {
	_, err := r.messages.InsertOne(ctx, msg)
	if err != nil {
		return err
	}
	log.Printf("✅ Message saved to MongoDB")
	return nil
}

// SaveRecord inserts a group join record
func (r *MongoRepository) SaveRecord(ctx context.Context, record *RecordDoc) error {
	_, err := r.records.InsertOne(ctx, record)
	if err != nil {
		return err
	}
	log.Printf("✅ Record saved to MongoDB")
	return nil
}

// FindMessages returns messages in the given date range, sorted by timestamp ascending
func (r *MongoRepository) FindMessages(ctx context.Context, start, end *time.Time) ([]*MessageDoc, error) {
	filter := bson.M{}
	if start != nil || end != nil {
		tsFilter := bson.M{}
		if start != nil {
			tsFilter["$gte"] = *start
		}
		if end != nil {
			tsFilter["$lte"] = *end
		}
		filter["timestamp"] = tsFilter
	}
	opts := options.Find().SetSort(bson.D{{Key: "timestamp", Value: 1}})
	cursor, err := r.messages.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var results []*MessageDoc
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}
