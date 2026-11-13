package charging

import (
	"bytes"
	"context"
	"math"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	charging_dict "github.com/free5gc/chf/ccs_diameter/dict"
	"github.com/free5gc/chf/internal/testutil"
)

func TestAccountArithmeticAndInvalidRequests(t *testing.T) {
	request := accountRequest()
	response, remaining, err := calculateAccountResponse(request, 20)
	require.NoError(t, err)
	require.Zero(t, remaining)
	require.Equal(t, datatype.Unsigned64(20), response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets)
	require.NotNil(t, response.MultipleServicesCreditControl.FinalUnitIndication)
	request.RequestedAction = charging_datatype.REFUND_ACCOUNT
	response, remaining, err = calculateAccountResponse(request, 20)
	require.NoError(t, err)
	require.Equal(t, int64(60), remaining)
	require.Equal(t, datatype.Integer64(60), response.RemainingBalance.UnitValue.ValueDigits)
	_, _, err = calculateAccountResponse(request, math.MaxInt64)
	require.Error(t, err)
	request.MultipleServicesCreditControl.RequestedServiceUnit = nil
	_, _, err = calculateAccountResponse(request, 20)
	require.Error(t, err)
	request.RequestedAction = charging_datatype.DIRECT_DEBITING
	request.CcRequestType = charging_datatype.TERMINATION_REQUEST
	request.MultipleServicesCreditControl.UsedServiceUnit = &charging_datatype.UsedServiceUnit{CCTotalOctets: 40}
	_, remaining, err = calculateAccountResponse(request, 20)
	require.NoError(t, err)
	require.Equal(t, int64(-20), remaining)
	_, _, err = calculateAccountResponse(request, math.MinInt64)
	require.Error(t, err)
}

func accountRequest() *charging_datatype.AccountDebitRequest {
	return &charging_datatype.AccountDebitRequest{
		SessionId:  datatype.UTF8String("chf;" + strconv.FormatInt(time.Now().Unix(), 10) + ";session-a"),
		OriginHost: "chf", CcRequestNumber: 0,
		EventTimestamp: datatype.Time(time.Now().UTC().Truncate(time.Second)),
		CcRequestType:  charging_datatype.UPDATE_REQUEST, RequestedAction: charging_datatype.DIRECT_DEBITING,
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

var accountWireDictionary sync.Once

func accountWireRequest(
	t *testing.T, request *charging_datatype.AccountDebitRequest,
) *charging_datatype.AccountDebitRequest {
	t.Helper()
	accountWireDictionary.Do(func() {
		require.NoError(t, dict.Default.Load(bytes.NewReader([]byte(charging_dict.AbmfDictionary))))
	})
	message := diam.NewRequest(charging_code.ABMF_CreditControl, charging_code.Re_interface, dict.Default)
	require.NoError(t, message.Marshal(request))
	encoded, err := message.Serialize()
	require.NoError(t, err)
	wireMessage, err := diam.ReadMessage(bytes.NewReader(encoded), dict.Default)
	require.NoError(t, err)
	var decoded charging_datatype.AccountDebitRequest
	require.NoError(t, wireMessage.Unmarshal(&decoded))
	return &decoded
}

func TestAccountIdentitySurvivesDiameterEncoding(t *testing.T) {
	for _, operation := range []string{"reserve", "refund", "debit"} {
		t.Run(operation, func(t *testing.T) {
			request := accountRequest()
			switch operation {
			case "refund":
				request.RequestedAction = charging_datatype.REFUND_ACCOUNT
			case "debit":
				request.CcRequestType = charging_datatype.TERMINATION_REQUEST
				request.MultipleServicesCreditControl.RequestedServiceUnit = nil
				request.MultipleServicesCreditControl.UsedServiceUnit = &charging_datatype.UsedServiceUnit{CCTotalOctets: 40}
			}
			decoded := accountWireRequest(t, request)
			require.Nil(t, decoded.MultipleServicesCreditControl.GSUPoolReference,
				"an unused optional pool reference must stay absent on the wire")
			beforeSubscriber, beforeID, beforeFingerprint, err := accountIdentity(request)
			require.NoError(t, err)
			afterSubscriber, afterID, afterFingerprint, err := accountIdentity(decoded)
			require.NoError(t, err)
			require.Equal(t, beforeSubscriber, afterSubscriber)
			require.Equal(t, beforeID, afterID)
			require.Equal(t, beforeFingerprint, afterFingerprint)
		})
	}
}

func seedPolicyAccount(t *testing.T, db *mongo.Database, quota string) {
	t.Helper()
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
		"ueId": "imsi-208930000001226", "ratingGroup": 10, "quota": quota,
	})
	require.NoError(t, err)
}

func policyBalance(t *testing.T, db *mongo.Database) string {
	t.Helper()
	var result account
	require.NoError(t, db.Collection("policyData.ues.chargingData").FindOne(context.Background(), bson.M{
		"ueId": "imsi-208930000001226", "ratingGroup": 10,
	}).Decode(&result))
	return result.Quota
}

func TestMongoAccountReplayIsLocalAndConcurrentDuplicatesDebitOnce(t *testing.T) {
	db := testutil.Mongo(t)
	seedPolicyAccount(t, db, "100")
	accounts := NewAccounts(db)
	t.Cleanup(accounts.Stop)
	request := accountRequest()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			response, err := accounts.Apply(context.Background(), accountWireRequest(t, request))
			require.NoError(t, err)
			require.Equal(t, datatype.Unsigned64(40), response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets)
		})
	}
	wg.Wait()
	require.Equal(t, "60", policyBalance(t, db))
	request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets++
	_, err := accounts.Apply(context.Background(), request)
	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, "60", policyBalance(t, db))
	names, err := db.ListCollectionNames(context.Background(), bson.M{})
	require.NoError(t, err)
	require.Equal(t, []string{"policyData.ues.chargingData"}, names, "no account copy or replay collection is created")
}

func TestMongoAccountUsesWebconsoleBalanceChangesAndRefunds(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedPolicyAccount(t, db, "40")
	accounts := NewAccounts(db)
	t.Cleanup(accounts.Stop)
	first := accountRequest()
	_, err := accounts.Apply(ctx, first)
	require.NoError(t, err)
	require.Equal(t, "0", policyBalance(t, db))
	// Webconsole changes the existing policy document in place.
	_, err = db.Collection("policyData.ues.chargingData").UpdateOne(ctx,
		bson.M{"ratingGroup": 10}, bson.M{"$set": bson.M{"quota": "100"}})
	require.NoError(t, err)
	_, err = accounts.Apply(ctx, first)
	require.NoError(t, err)
	require.Equal(t, "100", policyBalance(t, db), "replay must not debit the recharged balance again")
	next := accountRequest()
	next.CcRequestNumber++
	response, err := accounts.Apply(ctx, next)
	require.NoError(t, err)
	require.Equal(t, datatype.Unsigned64(40), response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets)
	require.Equal(t, "60", policyBalance(t, db))
	next = accountRequest()
	next.CcRequestNumber = 2
	next.RequestedAction = charging_datatype.REFUND_ACCOUNT
	for range 2 {
		_, err = accounts.Apply(ctx, next)
		require.NoError(t, err)
	}
	require.Equal(t, "100", policyBalance(t, db))
	// An old immutable identity is rejected even if its event timestamp is refreshed.
	expired := *next
	expired.SessionId = datatype.UTF8String("chf;" +
		strconv.FormatInt(time.Now().Add(-AccountReplayWindow).Unix(), 10) + ";session-a")
	_, err = accounts.Apply(ctx, &expired)
	require.ErrorIs(t, err, ErrExpired)
	require.Equal(t, "100", policyBalance(t, db), "expired refunds cannot increase the balance twice")
	fresh := accountRequest()
	fresh.CcRequestNumber = 3
	_, err = accounts.Apply(ctx, fresh)
	require.NoError(t, err)
	require.Equal(t, "60", policyBalance(t, db), "new operations still read the original policy balance")
}

func TestAccountCacheExpiresWithoutAllowingOldDebits(t *testing.T) {
	for _, outcome := range []string{"success", "unknown write"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				request := accountRequest()
				_, id, fingerprint, err := accountIdentity(request)
				require.NoError(t, err)
				response, _, err := calculateAccountResponse(request, 100)
				require.NoError(t, err)
				body, err := EncodeSnapshot(response)
				require.NoError(t, err)
				issued := time.Now()
				accounts := &Accounts{results: make(map[string]*accountResult)}
				defer accounts.Stop()
				result := &accountResult{fingerprint: fingerprint, body: body, expires: issued.Add(AccountReplayWindow)}
				if outcome == "unknown write" {
					result.err = ErrPending
				}
				// Time spent executing the debit does not extend its retry identity.
				time.Sleep(time.Minute)
				accounts.mu.Lock()
				accounts.cacheResult(id, result)
				accounts.mu.Unlock()
				time.Sleep(AccountReplayWindow/2 - time.Minute)
				replayed, err := accounts.Apply(context.Background(), request)
				if result.err != nil {
					require.ErrorIs(t, err, ErrPending, "unknown writes cannot be performed again")
				} else {
					require.NoError(t, err)
					require.Equal(t, datatype.Integer64(60), replayed.RemainingBalance.UnitValue.ValueDigits)
				}
				// No requests or sweeps are needed; replay does not reset the deadline.
				time.Sleep(AccountReplayWindow / 2)
				synctest.Wait()
				accounts.mu.Lock()
				empty := len(accounts.results) == 0
				accounts.mu.Unlock()
				require.True(t, empty, "idle result must expire at its original deadline")
				_, err = accounts.Apply(context.Background(), request)
				require.ErrorIs(t, err, ErrExpired, "an expired debit must not execute again")
				request.EventTimestamp = datatype.Time(time.Now())
				_, err = accounts.Apply(context.Background(), request)
				require.ErrorIs(t, err, ErrExpired, "refreshing EventTimestamp cannot reopen an old identity")
			})
		})
	}
}

func TestAccountCacheCapacityPreservesUnexpiredResults(t *testing.T) {
	accounts := &Accounts{results: make(map[string]*accountResult)}
	defer accounts.Stop()
	for sequence := range MaxAccountResults {
		accounts.results[strconv.Itoa(sequence)] = &accountResult{expires: time.Now().Add(AccountReplayWindow)}
	}
	_, err := accounts.Apply(context.Background(), accountRequest())
	require.ErrorIs(t, err, ErrCapacity, "capacity must not evict unexpired debit outcomes")
}

func TestAccountCacheReplacementAndShutdownCancelTimers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		accounts := &Accounts{results: make(map[string]*accountResult)}
		defer accounts.Stop()
		first := &accountResult{expires: time.Now().Add(time.Minute)}
		accounts.mu.Lock()
		accounts.cacheResult("operation", first)
		accounts.mu.Unlock()
		time.Sleep(30 * time.Second)
		replacement := &accountResult{expires: time.Now().Add(AccountReplayWindow)}
		accounts.mu.Lock()
		accounts.cacheResult("operation", replacement)
		accounts.mu.Unlock()
		// A callback dispatched before cancellation cannot remove the replacement.
		accounts.expire("operation", first)
		time.Sleep(30 * time.Second)
		synctest.Wait()
		accounts.mu.Lock()
		current := accounts.results["operation"]
		accounts.mu.Unlock()
		require.Same(t, replacement, current)
		require.False(t, first.timer.Stop(), "replacement must cancel the old timer")
		accounts.Stop()
		require.False(t, replacement.timer.Stop(), "shutdown must cancel pending timers")
		accounts.expire("operation", replacement)
		time.Sleep(AccountReplayWindow)
		synctest.Wait()
		require.Empty(t, accounts.results)
		_, err := accounts.Apply(context.Background(), accountRequest())
		require.ErrorIs(t, err, ErrStopped, "shutdown must reject operations before touching MongoDB")
	})
}
