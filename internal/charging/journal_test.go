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
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo/options"

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

func saveQuotaTransition(
	t *testing.T, j *MongoJournal, record *Record, index int, key QuotaKey, before, after QuotaState,
) {
	t.Helper()
	require.NoError(t, j.SaveSettlementStep(context.Background(), record, &SettlementStep{
		UsageIndex: index, QuotaKey: key, RatingGroup: key.RatingGroup, UPFID: key.UPFID,
		Before: before, Planned: after,
	}))
	require.NoError(t, j.SaveSettlementOutcome(context.Background(), record, index, &SettlementOutcome{
		QuotaKey: key, After: after,
	}))
}

func TestMongoJournalQuotaApplyIsAtomicOrderedAndSurvivesRestart(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	j := NewJournal(db)
	identity := Identity{Session: "quota-session", Sequence: 1, Operation: "update", Fingerprint: "first"}
	record, _, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	key := QuotaKey{ChargingDataRef: record.Session, RatingGroup: 10, UPFID: "upf-a"}
	reserved := QuotaState{Known: true, Reserved: 100, RatingType: charging_datatype.REQ_SUBTYPE_RESERVE, UnitCost: 1}
	consumed := reserved
	consumed.Reserved = 60
	saveQuotaTransition(t, j, record, 0, key, QuotaState{}, reserved)
	saveQuotaTransition(t, j, record, 2, key, reserved, consumed)
	_, err = j.ApplyQuota(ctx, record, 2)
	require.ErrorIs(t, err, ErrConflict, "later steps cannot bypass an unapplied earlier step")
	states, err := j.LoadQuotas(ctx, record.Session)
	require.NoError(t, err)
	require.Empty(t, states)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			state, applyErr := NewJournal(db).ApplyQuota(ctx, record, 0)
			require.NoError(t, applyErr)
			require.Equal(t, reserved, state)
		})
	}
	wg.Wait()
	state, err := j.ApplyQuota(ctx, record, 2)
	require.NoError(t, err)
	require.Equal(t, consumed, state)
	// A lost acknowledgement followed by a restart must not consume the 40 again,
	// nor may replaying step 0 restore its older reservation of 100.
	require.NoError(t, j.MarkUncertain(ctx, record, "crash after quota commit"))
	j = NewJournal(db)
	expireRecoveryLease(t, j, record)
	record, err = j.ClaimRecovery(ctx)
	require.NoError(t, err)
	require.NotNil(t, record)
	for _, index := range []int{0, 2, 2} {
		state, err = j.ApplyQuota(ctx, record, index)
		require.NoError(t, err)
		require.Equal(t, consumed, state)
	}
	states, err = j.LoadQuotas(ctx, record.Session)
	require.NoError(t, err)
	require.Equal(t, consumed, states[key])
	stored, err := j.loadQuotaSession(ctx, record.Session)
	require.NoError(t, err)
	require.Len(t, stored.Applied, 2)
	require.NotEmpty(t, stored.Applied["0"])
	require.NotEmpty(t, stored.Applied["2"])
	identity.Sequence++
	_, _, err = j.Begin(ctx, identity)
	require.ErrorIs(t, err, ErrPending, "quota commit alone does not complete accounting/CDR processing")
}

func TestMongoJournalQuotaRejectsStaleStateAndOldInvocation(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	j := NewJournal(db)
	identity := Identity{Session: "quota-session", Sequence: 1, Operation: "update", Fingerprint: "first"}
	first, _, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	key := QuotaKey{ChargingDataRef: first.Session, RatingGroup: 10, UPFID: "upf-a"}
	before := QuotaState{Known: true, Reserved: 100, RatingType: charging_datatype.REQ_SUBTYPE_RESERVE}
	saveQuotaTransition(t, j, first, 0, key, QuotaState{}, before)
	_, err = j.ApplyQuota(ctx, first, 0)
	require.NoError(t, err)
	require.NoError(t, j.Finish(ctx, first, Result{Status: 200}, Succeeded))
	identity.Sequence++
	second, _, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	_, err = j.ApplyQuota(ctx, first, 0)
	require.ErrorIs(t, err, ErrPending)
	stale := before
	stale.Reserved = 200
	after := before
	after.Reserved = 60
	saveQuotaTransition(t, j, second, 0, key, stale, after)
	_, err = j.ApplyQuota(ctx, second, 0)
	require.ErrorIs(t, err, ErrConflict)
	stored, err := j.loadQuotaSession(ctx, second.Session)
	require.NoError(t, err)
	require.Empty(t, stored.Applied, "a new invocation has its own bounded marker set")
	states, err := j.LoadQuotas(ctx, second.Session)
	require.NoError(t, err)
	require.Equal(t, before, states[key], "a stale plan cannot overwrite authoritative quota")
}

func TestMongoJournalRechargeWaitsForFrozenSettlementAndSurvivesRestart(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	j := NewJournal(db)
	identity := Identity{Session: "recharge-session", Sequence: 1, Operation: "update", Fingerprint: "first"}
	first, _, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	live := QuotaKey{ChargingDataRef: first.Session, RatingGroup: 10, UPFID: "upf-a"}
	closed := live
	closed.UPFID = "upf-closed"
	other := live
	other.RatingGroup = 20
	exhausted := QuotaState{Known: true, Exhausted: true, RatingType: charging_datatype.REQ_SUBTYPE_DEBIT}
	closedState := exhausted
	closedState.Closed = true
	for index, quota := range []struct {
		key   QuotaKey
		state QuotaState
	}{{live, exhausted}, {closed, closedState}, {other, exhausted}} {
		saveQuotaTransition(t, j, first, index, quota.key, QuotaState{}, quota.state)
		_, err = j.ApplyQuota(ctx, first, index)
		require.NoError(t, err)
	}
	// The notice is durable, but cannot change Before for an already-frozen step.
	require.NoError(t, j.QueueRecharge(ctx, first.Session, 10))
	j = NewJournal(db)
	states, err := j.PrepareQuotas(ctx, first)
	require.NoError(t, err)
	require.Equal(t, exhausted, states[live])
	stored, err := j.loadQuotaSession(ctx, first.Session)
	require.NoError(t, err)
	require.True(t, stored.Recharges["10"])
	require.NoError(t, j.Finish(ctx, first, Result{Status: 200}, Succeeded))
	identity.Sequence++
	second, _, err := j.Begin(ctx, identity)
	require.NoError(t, err)
	states, err = j.PrepareQuotas(ctx, second)
	require.NoError(t, err)
	require.False(t, states[live].Exhausted)
	require.Equal(t, charging_datatype.REQ_SUBTYPE_RESERVE, states[live].RatingType)
	require.Equal(t, closedState, states[closed])
	require.Equal(t, exhausted, states[other])
	stored, err = j.loadQuotaSession(ctx, second.Session)
	require.NoError(t, err)
	require.Empty(t, stored.Recharges)
	states, err = NewJournal(db).LoadQuotas(ctx, second.Session)
	require.NoError(t, err)
	require.False(t, states[live].Exhausted, "recharge effects survive loss of the live UE quota map")
}

func TestMongoJournalFreshQuotaUsesAcknowledgedSnapshotsUntilRecovery(t *testing.T) {
	var requestReads, sessionReads atomic.Int32
	monitor := &event.CommandMonitor{Started: func(_ context.Context, event *event.CommandStartedEvent) {
		if event.CommandName != "find" {
			return
		}
		collection, ok := event.Command.Lookup("find").StringValueOK()
		if !ok {
			return
		}
		switch collection {
		case "chf.chargingRequests":
			requestReads.Add(1)
		case "chf.chargingSessions":
			sessionReads.Add(1)
		}
	}}
	db := testutil.Mongo(t, options.Client().SetMonitor(monitor))
	ctx := context.Background()
	j := NewJournal(db)
	record, _, err := j.Begin(ctx, Identity{
		Session: "fresh-quota", Sequence: 1, Operation: "update", Fingerprint: "first",
	})
	require.NoError(t, err)
	requestReads.Store(0)
	sessionReads.Store(0)
	states, err := j.PrepareQuotas(ctx, record)
	require.NoError(t, err)
	require.Empty(t, states)
	require.Zero(t, requestReads.Load(), "a fresh invocation does not reload its request")
	require.Equal(t, int32(1), sessionReads.Load(), "authoritative quota still comes from the session document")
	key := QuotaKey{ChargingDataRef: record.Session, RatingGroup: 10, UPFID: "upf-a"}
	step := &SettlementStep{UsageIndex: 0, QuotaKey: key, RatingGroup: 10, UPFID: key.UPFID}
	outcome := &SettlementOutcome{QuotaKey: key, After: QuotaState{Known: true, Reserved: 100}}
	require.NoError(t, j.SaveSettlementStep(ctx, record, step))
	// Caller-owned data is not evidence that an outcome was persisted.
	body, err := chargingdb.EncodeSnapshot(outcome)
	require.NoError(t, err)
	record.SettlementPlan = &SettlementPlan{Outcomes: map[string][]byte{"0": body}}
	_, err = j.ApplyQuota(ctx, record, 0)
	require.Error(t, err, "an unsaved outcome cannot be applied through a caller-supplied plan")
	require.NoError(t, j.SaveSettlementOutcome(ctx, record, 0, outcome))
	// A failed attempt to overwrite the snapshot must not replace the cached
	// acknowledged bytes. Mutating caller pointers afterwards must not do so either.
	outcome.After.Reserved = 999
	require.ErrorIs(t, j.SaveSettlementOutcome(ctx, record, 0, outcome), ErrConflict)
	step.QuotaKey.UPFID = "changed-after-save"
	requestReads.Store(0)
	sessionReads.Store(0)
	state, err := j.ApplyQuota(ctx, record, 0)
	require.NoError(t, err)
	require.Equal(t, int64(100), state.Reserved)
	require.Zero(t, requestReads.Load(), "normal apply reuses immutable acknowledged bytes")
	require.Zero(t, sessionReads.Load(), "normal apply needs only the atomic session update")
	require.NoError(t, j.MarkUncertain(ctx, record, "acknowledgement lost"))
	expireRecoveryLease(t, j, record)
	record, err = j.ClaimRecovery(ctx)
	require.NoError(t, err)
	requestReads.Store(0)
	state, err = j.ApplyQuota(ctx, record, 0)
	require.NoError(t, err)
	require.Equal(t, int64(100), state.Reserved)
	require.Positive(t, requestReads.Load(), "uncertain invocations reload the durable plan even in the same process")
}

func expireRecoveryLease(t *testing.T, j *MongoJournal, record *Record) {
	t.Helper()
	past := time.Now().UTC().Add(-time.Minute)
	_, err := j.sessions.UpdateOne(context.Background(), bson.M{"_id": record.Session}, bson.M{"$set": bson.M{
		"leaseUntil": past, "readyAt": past,
	}})
	require.NoError(t, err)
	_, err = j.requests.UpdateOne(context.Background(), bson.M{"_id": record.ID},
		bson.M{"$set": bson.M{"leaseUntil": past}})
	require.NoError(t, err)
}

func TestMongoRecoveryLeaseFencesOldExecutorsAndBacksOff(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	j := NewJournal(db)
	require.NoError(t, chargingdb.InitializeSchema(ctx, db))
	original, _, err := j.Begin(ctx, Identity{
		Session: "lease-session", Sequence: 7, Operation: "update", Fingerprint: "usage",
	})
	require.NoError(t, err)
	key := QuotaKey{ChargingDataRef: original.Session, RatingGroup: 10, UPFID: "upf-a"}
	saveQuotaTransition(t, j, original, 0, key, QuotaState{}, QuotaState{Known: true, Reserved: 60})
	_, err = j.ApplyQuota(ctx, original, 0)
	require.NoError(t, err)
	none, err := j.ClaimRecovery(ctx)
	require.NoError(t, err)
	require.Nil(t, none, "live requests cannot be reclaimed")
	expireRecoveryLease(t, j, original)
	var winners atomic.Int32
	var winner *Record
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			claimed, claimErr := NewJournal(db).ClaimRecovery(ctx)
			require.NoError(t, claimErr)
			if claimed != nil {
				winners.Add(1)
				winner = claimed
			}
		})
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	require.NotEqual(t, original.Owner, winner.Owner)
	require.ErrorIs(t, j.RenewLease(ctx, original), ErrPending)
	require.ErrorIs(t, j.SaveSettlementOutcome(ctx, original, 0, &SettlementOutcome{QuotaKey: key}), ErrPending)
	_, err = j.ApplyQuota(ctx, original, 0)
	require.ErrorIs(t, err, ErrPending)
	require.ErrorIs(t, j.Finish(ctx, original, Result{Status: 200}, Succeeded), ErrPending)
	require.ErrorIs(t, j.MarkUncertain(ctx, original, "late failure"), ErrPending)
	state, err := j.ApplyQuota(ctx, winner, 0)
	require.NoError(t, err)
	require.Equal(t, int64(60), state.Reserved)
	require.NoError(t, j.RenewLease(ctx, winner))
	require.NoError(t, j.MarkUncertain(ctx, winner, "RF unavailable"))
	none, err = j.ClaimRecovery(ctx)
	require.NoError(t, err)
	require.Nil(t, none, "failed work waits for its persisted backoff")
	var queued struct {
		ReadyAt time.Time `bson:"readyAt"`
		Active  string    `bson:"active"`
	}
	require.NoError(t, j.sessions.FindOne(ctx, bson.M{"_id": original.Session}).Decode(&queued))
	require.Equal(t, original.ID, queued.Active)
	require.True(t, queued.ReadyAt.After(time.Now()))
	expireRecoveryLease(t, j, winner)
	next, err := j.ClaimRecovery(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, next.Attempts)
	require.NoError(t, j.Finish(ctx, next, Result{Status: 200}, Succeeded))
	replay, fresh, err := j.Begin(ctx, original.Identity)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, Succeeded, replay.State)
}

func TestMongoRecoveryHealsCompletedFenceButRetainsMissingRequest(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "missing"}[missing], func(t *testing.T) {
			j := NewJournal(testutil.Mongo(t))
			ctx := context.Background()
			record, _, err := j.Begin(ctx, Identity{Session: "fenced", Sequence: 1})
			require.NoError(t, err)
			if missing {
				_, err = j.requests.DeleteOne(ctx, bson.M{"_id": record.ID})
			} else {
				_, err = j.requests.UpdateOne(ctx, bson.M{"_id": record.ID}, bson.M{"$set": bson.M{"state": Succeeded}})
			}
			require.NoError(t, err)
			expireRecoveryLease(t, j, record)
			claimed, err := j.ClaimRecovery(ctx)
			require.Nil(t, claimed)
			if missing {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			session, loadErr := j.loadQuotaSession(ctx, record.Session)
			require.NoError(t, loadErr)
			if missing {
				require.Equal(t, record.ID, *session.Active)
			} else {
				require.Nil(t, session.Active)
			}
		})
	}
}
