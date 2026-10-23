// Package db manages MongoDB schema and collection settings.
package db

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// InitializeSchema prepares charging storage before importing accounts or serving requests.
func InitializeSchema(ctx context.Context, db *mongo.Database) error {
	_, err := DurableCollection(db, "chf.accounts").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "ueId", Value: 1}, {Key: "ratingGroup", Value: 1}}},
		{
			Keys:    bson.D{{Key: "chargingOperations.id", Value: 1}},
			Options: options.Index().SetUnique(true).SetSparse(true),
		},
	})
	if err != nil {
		return err
	}
	if _, err = DurableCollection(db, "chf.chargingSessions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "cdrSubscriber", Value: 1}}},
		{
			Keys:    bson.D{{Key: "readyAt", Value: 1}},
			Options: options.Index().SetPartialFilterExpression(bson.M{"active": bson.M{"$type": "string"}}),
		},
	}); err != nil {
		return err
	}
	_, err = DurableCollection(db, "chf.chargingRequests").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "session", Value: 1}, {Key: "state", Value: 1}},
	})
	return err
}

// DurableCollection applies majority read and write concerns to a collection.
func DurableCollection(db *mongo.Database, name string) *mongo.Collection {
	return db.Collection(name, options.Collection().
		SetWriteConcern(writeconcern.Majority()).SetReadConcern(readconcern.Majority()))
}
