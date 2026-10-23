package charging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	chargingdb "github.com/free5gc/chf/internal/db"
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

func TestMongoJournalSettlementSnapshotsAreImmutableAndSurviveRestart(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	journal := NewJournal(db)
	record, _, err := journal.Begin(ctx, Identity{
		Session: "session-plan", Sequence: 7, Operation: "update", Fingerprint: "original-request",
	})
	require.NoError(t, err)
	stamp := time.Date(2026, time.October, 22, 10, 0, 0, 123456789, time.UTC)
	request := accountRequest()
	request.EventTimestamp = datatype.Time(stamp)
	step := &SettlementStep{
		UsageIndex: 0, RatingGroup: 10, UPFID: "upf-a", AccountRequest: request,
		QuotaKey: QuotaKey{ChargingDataRef: record.Session, RatingGroup: 10, UPFID: "upf-a"},
		Before:   QuotaState{Known: true, Reserved: 100},
		Planned:  QuotaState{Known: true, Reserved: 0},
		Ratings: []RatingExchange{{
			Request: charging_datatype.ServiceUsageRequest{
				ActualTime: datatype.Time(stamp), BeginTime: datatype.Time(stamp.Add(-time.Hour)),
				ServiceRating: &charging_datatype.ServiceRating{ConsumedUnits: 40},
			},
			Response: charging_datatype.ServiceUsageResponse{
				EventTimestamp: datatype.Time(stamp),
				ServiceRating:  &charging_datatype.ServiceRating{Price: 40, ExpiryTime: datatype.Time(stamp.Add(time.Hour))},
			},
		}},
	}
	outcome := &SettlementOutcome{QuotaKey: step.QuotaKey, After: QuotaState{Known: true, Reserved: 0}}
	require.ErrorIs(t, journal.SaveSettlementOutcome(ctx, record, 0, outcome), ErrConflict)
	require.NoError(t, journal.SaveSettlementStep(ctx, record, step))
	require.NoError(t, journal.SaveSettlementStep(ctx, record, step))
	step.AccountRequest.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets++
	require.ErrorIs(t, journal.SaveSettlementStep(ctx, record, step), ErrConflict)
	step.AccountRequest.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets--
	require.NoError(t, journal.SaveSettlementOutcome(ctx, record, 0, outcome))
	require.NoError(t, journal.SaveSettlementOutcome(ctx, record, 0, outcome))
	outcome.After.Reserved = 99
	require.ErrorIs(t, journal.SaveSettlementOutcome(ctx, record, 0, outcome), ErrConflict)
	outcome.After.Reserved = 0
	require.NoError(t, journal.MarkUncertain(ctx, record, "interrupted after account answer"))
	stored, err := NewJournal(db).load(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, Uncertain, stored.State)
	var restored SettlementStep
	require.NoError(t, chargingdb.DecodeSnapshot(stored.SettlementPlan.Steps["0"], &restored))
	require.Equal(t, *step, restored, "all request, RF and timestamp fields must survive a process restart")
	var restoredOutcome SettlementOutcome
	require.NoError(t, chargingdb.DecodeSnapshot(stored.SettlementPlan.Outcomes["0"], &restoredOutcome))
	require.Equal(t, *outcome, restoredOutcome)
	require.ErrorIs(t, journal.SaveSettlementStep(ctx, record, step), ErrPending)
}
