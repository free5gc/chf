package charging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// LeaseDuration bounds execution ownership; heartbeats start after twenty seconds.
// Normal short requests acquire their lease in the existing Begin writes.
const LeaseDuration = time.Minute

// RecoveryJournal is optional for test journals; production uses MongoJournal.
type RecoveryJournal interface {
	Journal
	ClaimRecovery(context.Context) (*Record, error)
	RenewLease(context.Context, *Record) error
}

func requestLeaseFilter(record *Record) bson.M {
	return bson.M{
		"_id": record.ID, "state": Processing, "owner": record.Owner,
		"$expr": bson.M{"$gt": bson.A{"$leaseUntil", "$$NOW"}},
	}
}

func sessionLeaseFilter(record *Record) bson.M {
	return bson.M{
		"_id": record.Session, "active": record.ID, "owner": record.Owner,
		"$expr": bson.M{"$gt": bson.A{"$leaseUntil", "$$NOW"}},
	}
}

func ownsSession(session *quotaSession, record *Record) bool {
	return session.Active != nil && *session.Active == record.ID && session.Owner == record.Owner &&
		session.LeaseUntil.After(time.Now().UTC())
}

// ClaimRecovery transfers an expired resource lease first, then the request
// lease. All old writes check both their owner and expiry. A crash between the
// two writes leaves a closed fence until the new resource lease expires.
func (j *MongoJournal) ClaimRecovery(ctx context.Context) (*Record, error) {
	now := time.Now().UTC()
	owner, until := uuid.NewString(), now.Add(LeaseDuration)
	var session struct {
		Ref      string `bson:"_id"`
		Active   string `bson:"active"`
		Attempts int    `bson:"attempts"`
	}
	err := j.sessions.FindOneAndUpdate(ctx, bson.M{
		"active": bson.M{"$type": "string"}, "leaseUntil": bson.M{"$lte": now}, "readyAt": bson.M{"$lte": now},
	}, bson.M{"$set": bson.M{"owner": owner, "leaseUntil": until, "readyAt": until}, "$inc": bson.M{"attempts": 1}},
		options.FindOneAndUpdate().SetSort(bson.D{{Key: "readyAt", Value: 1}}).
			SetReturnDocument(options.After)).Decode(&session)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := j.load(ctx, session.Active)
	if err != nil {
		// A missing request can mean an ambiguous Begin insertion. Never clear it.
		return nil, fmt.Errorf("load fenced request %s: %w", session.Active, err)
	}
	if record.Session != session.Ref {
		return nil, ErrConflict
	}
	if record.State == Succeeded || record.State == Rejected {
		return nil, j.unlock(ctx, record)
	}
	filter := bson.M{
		"_id": record.ID, "state": bson.M{"$in": bson.A{Processing, Uncertain}},
		"leaseUntil": bson.M{"$lte": now},
	}
	result, err := j.requests.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"state": Processing, "owner": owner, "leaseUntil": until, "updatedAt": now,
	}})
	if err != nil {
		return nil, err
	}
	if result.MatchedCount != 1 {
		return nil, ErrPending
	}
	record.State, record.Owner, record.LeaseUntil, record.Attempts = Processing, owner, until, session.Attempts
	return record, nil
}

// Renew resource first, so recovery cannot take it while the request lease is
// being renewed. An expired lease is never resurrected by a late heartbeat.
func (j *MongoJournal) RenewLease(ctx context.Context, record *Record) error {
	until := time.Now().UTC().Add(LeaseDuration)
	for _, item := range []struct {
		collection *mongo.Collection
		filter     bson.M
	}{
		{j.sessions, sessionLeaseFilter(record)}, {j.requests, requestLeaseFilter(record)},
	} {
		renewed := bson.M{"leaseUntil": until}
		if item.collection == j.sessions {
			renewed["readyAt"] = until
		}
		result, err := item.collection.UpdateOne(ctx, item.filter, bson.M{"$set": renewed})
		if err != nil {
			return err
		}
		if result.MatchedCount != 1 {
			return ErrPending
		}
	}
	return nil
}

func recoveryDelay(attempt int) time.Duration {
	delay := 2 * time.Second
	for i := 0; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	return min(delay, 5*time.Minute)
}

// Persist uncertainty before making the session eligible for a retry. If either
// acknowledgement is lost, expiry still permits recovery without losing active.
func (j *MongoJournal) retryRecovery(ctx context.Context, record *Record, detail string) error {
	record.localJournal, record.localPlan = nil, nil
	now := time.Now().UTC()
	result, err := j.requests.UpdateOne(ctx, requestLeaseFilter(record), bson.M{"$set": bson.M{
		"state": Uncertain, "detail": detail, "updatedAt": now, "leaseUntil": now,
	}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrPending
	}
	result, err = j.sessions.UpdateOne(ctx, sessionLeaseFilter(record), bson.M{"$set": bson.M{
		"leaseUntil": now, "readyAt": now.Add(recoveryDelay(record.Attempts)),
	}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrPending
	}
	return nil
}
