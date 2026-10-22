package charging

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
)

func testMongo(t *testing.T) *mongo.Database {
	t.Helper()
	uri := os.Getenv("CHF_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set CHF_TEST_MONGO_URI to run isolated MongoDB integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	db := client.Database("chf_test_" + uuid.NewString())
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		require.NoError(t, db.Drop(closeCtx))
		require.NoError(t, client.Disconnect(closeCtx))
	})
	require.NoError(t, client.Ping(ctx, nil))
	return db
}

func TestMongoJournalReplayAndIdentity(t *testing.T) {
	db := testMongo(t)
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
			db := testMongo(t)
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
	db := testMongo(t)
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
	db := testMongo(t)
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

func accountRequest() *charging_datatype.AccountDebitRequest {
	return &charging_datatype.AccountDebitRequest{
		SessionId: "chf;session-a;7;0", OriginHost: "chf", CcRequestNumber: 0,
		CcRequestType: charging_datatype.UPDATE_REQUEST, RequestedAction: charging_datatype.DIRECT_DEBITING,
		SubscriptionId: &charging_datatype.SubscriptionId{
			SubscriptionIdType: charging_datatype.END_USER_IMSI,
			SubscriptionIdData: "208930000001226",
		},
		MultipleServicesCreditControl: &charging_datatype.MultipleServicesCreditControl{
			RatingGroup:          10,
			RequestedServiceUnit: &charging_datatype.RequestedServiceUnit{CCTotalOctets: 40},
		},
	}
}

func seedAccount(t *testing.T, db *mongo.Database, quota string) {
	t.Helper()
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(),
		bson.M{"ueId": "imsi-208930000001226", "ratingGroup": 10, "quota": quota})
	require.NoError(t, err)
}

func readBalance(t *testing.T, db *mongo.Database) account {
	t.Helper()
	var result account
	err := db.Collection("policyData.ues.chargingData").FindOne(context.Background(),
		bson.M{"ueId": "imsi-208930000001226", "ratingGroup": 10}).Decode(&result)
	require.NoError(t, err)
	return result
}

func TestMongoAccountDebitAndRefundReplayAfterRestart(t *testing.T) {
	db := testMongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	request := accountRequest()
	first, err := NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "60", readBalance(t, db).Quota)
	request.EventTimestamp = datatype.Time(time.Now().Add(time.Hour))
	replay, err := NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.Equal(t, "60", readBalance(t, db).Quota)
	request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets = 99
	_, err = NewAccounts(db).Apply(ctx, request)
	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, "60", readBalance(t, db).Quota)
	request.SessionId = "chf;session-a;8;0"
	request.RequestedAction = charging_datatype.REFUND_ACCOUNT
	request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets = 10
	first, err = NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	replay, err = NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.Equal(t, "70", readBalance(t, db).Quota)
	require.Len(t, readBalance(t, db).Operations, 2)
}

func TestMongoAccountConcurrentDuplicateDebit(t *testing.T) {
	db := testMongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			response, err := NewAccounts(db).Apply(ctx, accountRequest())
			require.NoError(t, err)
			require.Equal(t, datatype.Unsigned64(40), response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets)
		})
	}
	wg.Wait()
	balance := readBalance(t, db)
	require.Equal(t, "60", balance.Quota)
	require.Len(t, balance.Operations, 1)
}

func TestMongoAccountConcurrentDistinctOperationsPreserveBalance(t *testing.T) {
	db := testMongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		sequence := i
		wg.Go(func() {
			request := accountRequest()
			request.CcRequestNumber = datatype.Unsigned32(sequence)
			request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets = 1
			_, err := NewAccounts(db).Apply(ctx, request)
			require.NoError(t, err)
		})
	}
	wg.Wait()
	balance := readBalance(t, db)
	require.Equal(t, "88", balance.Quota)
	require.Len(t, balance.Operations, 12)
}

func TestMongoAccountArchivedIdentityStillPreventsDebit(t *testing.T) {
	db := testMongo(t)
	ctx := context.Background()
	seedAccount(t, db, "1000")
	request := accountRequest()
	request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets = 1
	first, err := NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	for sequence := 1; sequence <= maxAccountOperations; sequence++ {
		request.CcRequestNumber = datatype.Unsigned32(sequence)
		_, err = NewAccounts(db).Apply(ctx, request)
		require.NoError(t, err)
	}
	balance := readBalance(t, db)
	require.Equal(t, "871", balance.Quota)
	require.Less(t, len(balance.Operations), maxAccountOperations)
	request.CcRequestNumber = 0
	replay, err := NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.Equal(t, "871", readBalance(t, db).Quota)
}
