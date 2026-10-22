// Package charging stores charging operation identities independently of live UE contexts.
package charging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

var (
	ErrConflict = errors.New("charging invocation reused with different content")
	ErrPending  = errors.New("charging session has an unresolved operation")
)

const (
	Processing = "processing"
	Succeeded  = "succeeded"
	Rejected   = "rejected"
	Uncertain  = "uncertain"
)

// Identity is scoped to the CHF resource, not just SUPI or rating group.
// Operation and fingerprint are checked separately to catch sequence reuse.
type Identity struct {
	Request     []byte `bson:"request"`
	Session     string `bson:"session"`
	Sequence    int32  `bson:"sequence"`
	Operation   string `bson:"operation"`
	Fingerprint string `bson:"fingerprint"`
}

func Digest(v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (i Identity) ID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", i.Session, i.Sequence)))
	return hex.EncodeToString(sum[:])
}

type Result struct {
	Status int    `bson:"status"`
	Body   []byte `bson:"body"`
}

type Record struct {
	ID        string `bson:"_id"`
	Identity  `bson:",inline"`
	State     string    `bson:"state"`
	Result    Result    `bson:"result"`
	Detail    string    `bson:"detail,omitempty"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

// Journal never expires unresolved operations or completed replay records.
// An interrupted operation is blocked for reconciliation, not executed again.
type Journal interface {
	Begin(context.Context, Identity) (*Record, bool, error)
	Finish(context.Context, *Record, Result, string) error
	MarkUncertain(context.Context, *Record, string) error
}

type MongoJournal struct {
	requests *mongo.Collection
	sessions *mongo.Collection
}

func durableCollection(db *mongo.Database, name string) *mongo.Collection {
	return db.Collection(name, options.Collection().
		SetWriteConcern(writeconcern.Majority()).SetReadConcern(readconcern.Majority()))
}

func NewJournal(db *mongo.Database) *MongoJournal {
	return &MongoJournal{
		requests: durableCollection(db, "chf.chargingRequests"),
		sessions: durableCollection(db, "chf.chargingSessions"),
	}
}

func (j *MongoJournal) load(ctx context.Context, id string) (*Record, error) {
	var record Record
	if err := j.requests.FindOne(ctx, bson.M{"_id": id}).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func checkRecord(record *Record, identity Identity) error {
	if record.Fingerprint != identity.Fingerprint || record.Operation != identity.Operation {
		return ErrConflict
	}
	if record.State != Succeeded && record.State != Rejected {
		return ErrPending
	}
	return nil
}

func (j *MongoJournal) unlock(ctx context.Context, record *Record) error {
	_, err := j.sessions.UpdateOne(ctx,
		bson.M{"_id": record.Session, "active": record.ID}, bson.M{"$set": bson.M{"active": nil}})
	return err
}

func (j *MongoJournal) Begin(ctx context.Context, identity Identity) (*Record, bool, error) {
	id := identity.ID()
	if record, err := j.load(ctx, id); err == nil {
		if err = checkRecord(record, identity); err != nil {
			return nil, false, err
		}
		if err = j.unlock(ctx, record); err != nil {
			return nil, false, err
		}
		return record, false, nil
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, err
	}

	// A separate session fence prevents a new sequence or Release bypassing an
	// ambiguous Update. Crashes between these writes leave the fence closed.
	_, err := j.sessions.UpdateOne(ctx, bson.M{"_id": identity.Session},
		bson.M{"$setOnInsert": bson.M{"active": nil}}, options.Update().SetUpsert(true))
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return nil, false, err
	}
	var session struct {
		Active *string `bson:"active"`
	}
	if err = j.sessions.FindOne(ctx, bson.M{"_id": identity.Session}).Decode(&session); err != nil {
		return nil, false, err
	}
	if session.Active != nil {
		previous, loadErr := j.load(ctx, *session.Active)
		if loadErr != nil || (previous.State != Succeeded && previous.State != Rejected) {
			return nil, false, ErrPending
		}
		if err = j.unlock(ctx, previous); err != nil {
			return nil, false, err
		}
	}
	claim, err := j.sessions.UpdateOne(ctx, bson.M{"_id": identity.Session, "active": nil},
		bson.M{"$set": bson.M{"active": id}})
	if err != nil {
		return nil, false, err
	}
	if claim.MatchedCount != 1 {
		return nil, false, ErrPending
	}
	record := &Record{ID: id, Identity: identity, State: Processing, UpdatedAt: time.Now().UTC()}
	_, err = j.requests.InsertOne(ctx, record)
	if mongo.IsDuplicateKeyError(err) {
		// A concurrent original invocation may have finished after the first lookup.
		record, err = j.load(ctx, id)
		if err != nil {
			return nil, false, err
		}
		if err = checkRecord(record, identity); err != nil {
			return nil, false, err
		}
		if err = j.unlock(ctx, record); err != nil {
			return nil, false, err
		}
		return record, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return record, true, nil
}

func (j *MongoJournal) Finish(ctx context.Context, record *Record, result Result, state string) error {
	if state != Succeeded && state != Rejected {
		return fmt.Errorf("invalid completed state %q", state)
	}
	update, err := j.requests.UpdateOne(ctx, bson.M{"_id": record.ID, "state": Processing},
		bson.M{"$set": bson.M{"state": state, "result": result, "updatedAt": time.Now().UTC()}})
	if err != nil {
		return err
	}
	if update.MatchedCount != 1 {
		return ErrPending
	}
	// Persist the response before releasing the fence, and before sending it to SMF.
	return j.unlock(ctx, record)
}

func (j *MongoJournal) MarkUncertain(ctx context.Context, record *Record, detail string) error {
	_, err := j.requests.UpdateOne(ctx, bson.M{"_id": record.ID, "state": Processing},
		bson.M{"$set": bson.M{"state": Uncertain, "detail": detail, "updatedAt": time.Now().UTC()}})
	return err
}
