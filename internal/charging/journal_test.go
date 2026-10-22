package charging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/free5gc/chf/internal/testutil"
)

func TestMongoJournalReplayAndIdentity(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	identity := Identity{Session: "session-a", Sequence: 7, Operation: "update", Fingerprint: "40MB"}
	j := NewJournal(db)
	record, fresh, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	require.True(t, fresh)
	_, _, err = j.Begin(ctx, identity)
	require.ErrorIs(t, err, ErrPending)
	result := Result{Status: 200, Body: []byte(`{"invocationSequenceNumber":7}`)}
	require.NoError(t, j.Finish(ctx, record, result, Succeeded))
	// Recreate the store, exactly as after process restart; no in-memory cache.
	replayed, fresh, err := NewJournal(db).Begin(ctx, identity)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, result, replayed.Result)
	changed := identity
	changed.Fingerprint = "60MB"
	_, _, err = j.Begin(ctx, changed)
	require.ErrorIs(t, err, ErrConflict)
	changed = identity
	changed.Operation = "release"
	_, _, err = j.Begin(ctx, changed)
	require.ErrorIs(t, err, ErrConflict)
}

func TestMongoJournalUncertainOperationFencesNewSequences(t *testing.T) {
	for _, mark := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted process", true: "known uncertainty"}[mark], func(t *testing.T) {
			db := testutil.Mongo(t)
			ctx := context.Background()
			j := NewJournal(db)
			identity := Identity{Session: "session-a", Sequence: 7, Operation: "update", Fingerprint: "40MB"}
			record, _, err := j.Begin(ctx, identity)
			require.NoError(t, err)
			if mark {
				require.NoError(t, j.MarkUncertain(ctx, record, "lost account answer"))
			}
			j = NewJournal(db)
			_, _, err = j.Begin(ctx, identity)
			require.ErrorIs(t, err, ErrPending)
			identity.Sequence++
			identity.Operation = "release"
			_, _, err = j.Begin(ctx, identity)
			require.ErrorIs(t, err, ErrPending)
		})
	}
}

func TestMongoJournalRecoversCompletedFenceAndKeepsInterruptedFence(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	j := NewJournal(db)
	identity := Identity{Session: "session-a", Sequence: 7, Operation: "update", Fingerprint: "40MB"}
	record, _, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	// Crash after persisting completion, before unlocking the session.
	_, err = j.requests.UpdateOne(ctx, bson.M{"_id": record.ID}, bson.M{"$set": bson.M{
		"state":  Succeeded,
		"result": Result{Status: 200, Body: []byte(`{}`)},
	}})
	require.NoError(t, err)
	identity.Sequence++
	_, fresh, err := NewJournal(db).Begin(ctx, identity)
	require.NoError(t, err)
	require.True(t, fresh)
	// Crash after claiming a different session, before inserting its request.
	_, err = j.sessions.InsertOne(ctx, bson.M{"_id": "interrupted-session", "active": "missing-request"})
	require.NoError(t, err)
	identity.Session = "interrupted-session"
	_, _, err = NewJournal(db).Begin(ctx, identity)
	require.ErrorIs(t, err, ErrPending)
}

func TestMongoJournalConcurrentClaimsExecuteOnce(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	identity := Identity{Session: "session-a", Sequence: 7, Operation: "update", Fingerprint: "40MB"}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			_, fresh, err := NewJournal(db).Begin(ctx, identity)
			if fresh {
				accepted.Add(1)
			}
			if err != nil {
				require.ErrorIs(t, err, ErrPending)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int32(1), accepted.Load())
}
