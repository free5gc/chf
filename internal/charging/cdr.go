package charging

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/free5gc/chf/cdr/cdrType"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/chf/internal/logger"
)

// Only the initial CDR is stored as a snapshot. Subsequent usage already lives
// in the request journal; completion stores just the metadata needed to replay it.
type CDRState struct {
	Subscriber string
	NotifyURI  string
	Current    *cdrType.CHFRecord
	Closed     bool
}

type CDRMetadata struct {
	Previous       string  `bson:"previous"`
	Partial        bool    `bson:"partial"`
	Stamp          string  `bson:"stamp"`
	LocalSequences []int64 `bson:"localSequences,omitempty"`
}

// CDRProgress is an in-memory projection cursor, not another database record.
type CDRProgress struct {
	EventID  string
	Closed   bool
	Metadata *CDRMetadata
}

type CDRHistory struct {
	Resource string
	Initial  CDRState
	Events   []Record
}

func CDRIdentity(record *cdrType.CHFRecord) (string, error) {
	if record == nil || record.ChargingFunctionRecord == nil ||
		record.ChargingFunctionRecord.ChargingSessionIdentifier == nil {
		return "", fmt.Errorf("CDR has no charging resource identity")
	}
	cdr := record.ChargingFunctionRecord
	sequence := int64(0)
	if cdr.RecordSequenceNumber != nil {
		sequence = *cdr.RecordSequenceNumber
	}
	return Digest([]any{string(cdr.ChargingSessionIdentifier.Value), sequence})
}

func (j *MongoJournal) InitializeCDR(ctx context.Context, ref string, state *CDRState) error {
	if _, err := CDRIdentity(state.Current); err != nil {
		return err
	}
	if state.Subscriber == "" || string(state.Current.ChargingFunctionRecord.ChargingSessionIdentifier.Value) != ref {
		return ErrConflict
	}
	body, err := chargingdb.EncodeSnapshot(state)
	if err != nil {
		return err
	}
	_, err = j.sessions.UpdateOne(ctx, bson.M{
		"_id": ref, "active": nil,
		"$or": bson.A{bson.M{"cdrInitial": bson.M{"$exists": false}}, bson.M{"cdrInitial": body}},
	}, bson.M{
		"$setOnInsert": bson.M{"active": nil},
		"$set":         bson.M{"cdrInitial": body, "cdrSubscriber": state.Subscriber},
	}, options.Update().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	return err
}

// History scans are confined to explicit reconstruction, never normal Update/Release.
func (j *MongoJournal) LoadCDRHistory(ctx context.Context, subscriber string) ([]CDRHistory, error) {
	cursor, err := j.sessions.Find(ctx, bson.M{"cdrSubscriber": subscriber}, options.Find().SetProjection(
		bson.M{"cdrInitial": 1},
	))
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := cursor.Close(ctx); closeErr != nil {
			logger.ChargingdataPostLog.Errorf("Close CDR history cursor: %v", closeErr)
		}
	}()
	var histories []CDRHistory
	for cursor.Next(ctx) {
		var source struct {
			Ref  string `bson:"_id"`
			Body []byte `bson:"cdrInitial"`
		}
		if err = cursor.Decode(&source); err != nil {
			return nil, err
		}
		history := CDRHistory{Resource: source.Ref}
		if err = chargingdb.DecodeSnapshot(source.Body, &history.Initial); err != nil {
			return nil, err
		}
		if _, err = CDRIdentity(history.Initial.Current); err != nil {
			return nil, err
		}
		if history.Initial.Subscriber != subscriber ||
			string(history.Initial.Current.ChargingFunctionRecord.ChargingSessionIdentifier.Value) != source.Ref {
			return nil, ErrConflict
		}
		events, findErr := j.requests.Find(ctx, bson.M{
			"session": source.Ref, "state": Succeeded, "cdrEvent": bson.M{"$exists": true},
		}, options.Find().SetProjection(bson.M{
			"request": 1, "session": 1, "sequence": 1, "operation": 1, "fingerprint": 1,
			"state": 1, "createdAt": 1, "cdrEvent": 1,
		}))
		if findErr != nil {
			return nil, findErr
		}
		if err = events.All(ctx, &history.Events); err != nil {
			return nil, err
		}
		histories = append(histories, history)
	}
	return histories, cursor.Err()
}
