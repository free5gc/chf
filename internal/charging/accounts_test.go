package charging

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
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
	chargingdb "github.com/free5gc/chf/internal/db"
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
	require.NoError(t, chargingdb.InitializeSchema(context.Background(), db))
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(),
		bson.M{"ueId": "imsi-208930000001226", "ratingGroup": 10, "quota": quota})
	require.NoError(t, err)
}

func readBalance(t *testing.T, db *mongo.Database) account {
	t.Helper()
	var result account
	err := db.Collection("chf.accounts").FindOne(context.Background(),
		bson.M{"ueId": "imsi-208930000001226", "ratingGroup": 10}).Decode(&result)
	require.NoError(t, err)
	return result
}

func TestMongoAccountDebitAndRefundReplayAfterRestart(t *testing.T) {
	db := testutil.Mongo(t)
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
	db := testutil.Mongo(t)
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
	db := testutil.Mongo(t)
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
	db := testutil.Mongo(t)
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
	_, err = db.Collection("policyData.ues.chargingData").DeleteMany(ctx, bson.M{})
	require.NoError(t, err)
	request.CcRequestNumber = 0
	replay, err := NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.Equal(t, "871", readBalance(t, db).Quota)
}

func TestMongoAccountSurvivesPolicyDeletion(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	accounts := NewAccounts(db)
	request := accountRequest()
	first, err := accounts.Apply(ctx, request)
	require.NoError(t, err)
	var policy account
	require.NoError(t, accounts.policies.FindOne(ctx, bson.M{}).Decode(&policy))
	require.Equal(t, "100", policy.Quota, "ABMF must not write the policy seed")
	require.Empty(t, policy.Operations)
	_, err = accounts.policies.DeleteMany(ctx, bson.M{"ratingGroup": 10})
	require.NoError(t, err)
	accounts = NewAccounts(db)
	replay, err := accounts.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	// A new final debit and refund use the retained account, even after restart.
	request.SessionId = "final-debit"
	request.CcRequestType = charging_datatype.TERMINATION_REQUEST
	request.MultipleServicesCreditControl.UsedServiceUnit = &charging_datatype.UsedServiceUnit{CCTotalOctets: 10}
	_, err = accounts.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "50", readBalance(t, db).Quota)
	request.SessionId = "refund"
	request.RequestedAction = charging_datatype.REFUND_ACCOUNT
	request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets = 20
	refund, err := accounts.Apply(ctx, request)
	require.NoError(t, err)
	replay, err = NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, refund, replay)
	require.Equal(t, "70", readBalance(t, db).Quota)
	require.Len(t, readBalance(t, db).Operations, 3)
	count, err := accounts.policies.CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	require.Zero(t, count, "settlement must not recreate a deleted policy")
}

func TestMongoAccountRatingGroupReuseKeepsGenerationsSeparate(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	accounts := NewAccounts(db)
	oldRequest := accountRequest()
	first, err := accounts.Apply(ctx, oldRequest)
	require.NoError(t, err)
	oldBalance := readBalance(t, db)
	_, err = accounts.policies.DeleteMany(ctx, bson.M{})
	require.NoError(t, err)
	seedAccount(t, db, "500") // Same UE and RG, a new PCF document _id.
	newRequest := accountRequest()
	newRequest.SessionId = "new-policy-operation"
	_, err = accounts.Apply(ctx, newRequest)
	require.NoError(t, err)
	var currentPolicy account
	require.NoError(t, accounts.policies.FindOne(ctx, bson.M{}).Decode(&currentPolicy))
	var newBalance account
	require.NoError(t, accounts.balances.FindOne(ctx, bson.M{"_id": currentPolicy.ID}).Decode(&newBalance))
	require.NotEqual(t, oldBalance.ID, newBalance.ID)
	require.Equal(t, "460", newBalance.Quota)
	// Old replay must find the old result before looking up today's policy.
	replay, err := NewAccounts(db).Apply(ctx, oldRequest)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.NoError(t, accounts.balances.FindOne(ctx, bson.M{"_id": oldBalance.ID}).Decode(&oldBalance))
	require.Equal(t, "60", oldBalance.Quota)
	_, err = accounts.policies.DeleteMany(ctx, bson.M{})
	require.NoError(t, err)
	newRequest.SessionId = "unbound-operation"
	_, err = accounts.Apply(ctx, newRequest)
	require.ErrorContains(t, err, "ambiguous account binding")
	require.NoError(t, accounts.balances.FindOne(ctx, bson.M{"_id": newBalance.ID}).Decode(&newBalance))
	require.Equal(t, "460", newBalance.Quota, "ambiguous settlement must not change any balance")
}

func TestMongoAccountImportsLegacyBalanceAndReplayHistoryOnce(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedAccount(t, db, "60")
	request := accountRequest()
	response, _, err := calculateAccountResponse(request, 100)
	require.NoError(t, err)
	body, err := json.Marshal(response)
	require.NoError(t, err)
	_, id, fingerprint, err := accountIdentity(request)
	require.NoError(t, err)
	accounts := NewAccounts(db)
	_, err = accounts.policies.UpdateOne(ctx, bson.M{}, bson.M{"$set": bson.M{
		"chargingVersion": 7,
		"chargingOperations": []accountOperation{{
			ID: id, Fingerprint: fingerprint, Response: body,
			EventTimestamp: time.Time(response.EventTimestamp).UnixNano(),
		}},
	}})
	require.NoError(t, err)
	require.NoError(t, accounts.ImportLegacy(ctx))
	replay, err := accounts.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, response, replay)
	require.Equal(t, "60", readBalance(t, db).Quota)
	require.EqualValues(t, 7, *readBalance(t, db).Version)
	request.SessionId = "after-import"
	_, err = accounts.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "20", readBalance(t, db).Quota)
	// Startup import must never overwrite the authoritative balance with a seed.
	require.NoError(t, accounts.ImportLegacy(ctx))
	request.SessionId = "after-restart"
	request.RequestedAction = charging_datatype.REFUND_ACCOUNT
	_, err = NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "60", readBalance(t, db).Quota)
	require.Len(t, readBalance(t, db).Operations, 3)
}

func TestMongoAccountMissingPolicyAndBalanceDoesNotCreateCredit(t *testing.T) {
	db := testutil.Mongo(t)
	_, err := NewAccounts(db).Apply(context.Background(), accountRequest())
	require.ErrorIs(t, err, mongo.ErrNoDocuments)
	count, err := db.Collection("chf.accounts").CountDocuments(context.Background(), bson.M{})
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestMongoAccountStartupImportRetainsUnusedAccounts(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	accounts := NewAccounts(db)
	// Provisioning templates without a rating group are not runtime accounts.
	_, err := accounts.policies.InsertOne(ctx, bson.M{"ueId": "template", "quota": "999"})
	require.NoError(t, err)
	require.NoError(t, accounts.ImportLegacy(ctx))
	count, err := accounts.balances.CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	_, err = accounts.policies.DeleteMany(ctx, bson.M{})
	require.NoError(t, err)
	_, err = NewAccounts(db).Apply(ctx, accountRequest())
	require.NoError(t, err)
	require.Equal(t, "60", readBalance(t, db).Quota)
}

func TestMongoAccountBindingSurvivesReuseBeforeFirstDebit(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	request := accountRequest()
	accounts := NewAccounts(db)
	bound, err := accounts.Bind(ctx, request)
	require.NoError(t, err)
	var oldAccount account
	require.NoError(t, bson.Unmarshal(bound, &oldAccount))
	require.Equal(t, "100", readBalance(t, db).Quota, "binding does not debit the account")
	_, err = accounts.policies.DeleteMany(ctx, bson.M{})
	require.NoError(t, err)
	seedAccount(t, db, "500")
	newRequest := accountRequest()
	newRequest.SessionId = "new-policy"
	_, err = accounts.Apply(ctx, newRequest)
	require.NoError(t, err)
	// Restart after the policy changed, before the old request ever debited.
	accounts = NewAccounts(db)
	first, err := accounts.Apply(ctx, request)
	require.NoError(t, err)
	replay, err := accounts.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.NoError(t, accounts.balances.FindOne(ctx, bson.M{"_id": oldAccount.ID}).Decode(&oldAccount))
	require.Equal(t, "60", oldAccount.Quota)
	var currentPolicy, newAccount account
	require.NoError(t, accounts.policies.FindOne(ctx, bson.M{}).Decode(&currentPolicy))
	require.NoError(t, accounts.balances.FindOne(ctx, bson.M{"_id": currentPolicy.ID}).Decode(&newAccount))
	require.Equal(t, "460", newAccount.Quota)
	request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets++
	_, err = accounts.Bind(ctx, request)
	require.ErrorIs(t, err, ErrConflict)
}

var accountWireDictionary sync.Once

// Use a real serialized CCR: in-memory account calls do not exercise optional
// grouped AVPs, whose decoded representation can differ from the original.
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

func TestMongoAccountBindingAcceptsWireRequestAndReplays(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	seedAccount(t, db, "100")
	request := accountRequest()
	_, err := NewAccounts(db).Bind(ctx, request)
	require.NoError(t, err)
	wireRequest := accountWireRequest(t, request)
	first, err := NewAccounts(db).Apply(ctx, wireRequest)
	require.NoError(t, err)
	require.Equal(t, datatype.Unsigned64(40), first.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets)
	require.Equal(t, "60", readBalance(t, db).Quota)
	// A restart/retransmission may switch between the frozen and decoded forms.
	replay, err := NewAccounts(db).Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, replay)
	require.Equal(t, "60", readBalance(t, db).Quota)
	wireRequest.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets++
	_, err = NewAccounts(db).Apply(ctx, wireRequest)
	require.ErrorIs(t, err, ErrConflict, "real changes to units must still be rejected")
	require.Equal(t, "60", readBalance(t, db).Quota)
}
