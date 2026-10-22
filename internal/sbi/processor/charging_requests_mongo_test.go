package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

func processorMongo(t *testing.T) *mongo.Database {
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

// The real processor, reservation logic, CDR writer, request journal and atomic
// account store run here. Only the Diameter network boundary and rating are stubbed.
func TestMongoChargingLostResponseReplaysWithoutAnotherDebit(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		reserved        int64
		remaining       string
		requesting      bool
	}{
		{"update debit", chargingUpdateOperation, 0, "60", false},
		{"release debit", chargingReleaseOperation, 0, "60", false},
		{"update refund", chargingUpdateOperation, 100, "160", false},
		{"release refund", chargingReleaseOperation, 100, "160", false},
		{"update reservation", chargingUpdateOperation, 0, "860", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation := tc.operation
			db := processorMongo(t)
			p, request, ref := chargingRequestSession(t)
			p.journal = charging.NewJournal(db)
			initialBalance := "100"
			if tc.requesting {
				initialBalance = "1000"
				request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
			}
			request.NfConsumerIdentification = &models.Chf_ConvCharging_NFIdentification{
				NodeFunctionality: models.Chf_ConvCharging_NodeFunctionality_SMF, NFName: "smf-test",
			}
			ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			require.True(t, ok)
			cdr, err := p.OpenCDR(request, ue, ref, false)
			require.NoError(t, err)
			ue.ReservedQuota[10] = tc.reserved
			ue.Cdr[ref] = cdr
			ue.Records = []*cdrType.CHFRecord{cdr}
			t.Cleanup(func() {
				removeErr := os.Remove(buildCdrFileName(request.SubscriberIdentifier))
				require.NoError(t, removeErr)
			})
			_, err = db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
				"ueId": request.SubscriberIdentifier, "ratingGroup": 10, "quota": initialBalance,
			})
			require.NoError(t, err)
			accounts := charging.NewAccounts(db)
			chf_context.GetSelf().AbmfCfg.OriginHost = "chf-test"
			calls := 0
			p.accountDebit = func(_ *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
				*charging_datatype.AccountDebitResponse, error,
			) {
				calls++
				return accounts.Apply(context.Background(), account)
			}
			p.serviceUsage = func(_ *chf_context.ChfUe, _ *charging_datatype.ServiceUsageRequest) (
				*charging_datatype.ServiceUsageResponse, error,
			) {
				return &charging_datatype.ServiceUsageResponse{ServiceRating: &charging_datatype.ServiceRating{
					Price: 40, AllowedUnits: 100,
					MonetaryTariff: &charging_datatype.MonetaryTariff{RateElement: &charging_datatype.RateElement{
						UnitCost: &charging_datatype.UnitCost{ValueDigits: 1},
					}},
				}}, nil
			}
			first := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(first)
			if operation == chargingUpdateOperation {
				p.HandleChargingdataUpdate(ctx, request, ref)
			} else {
				p.HandleChargingdataRelease(ctx, request, ref)
			}
			ctx.Writer.WriteHeaderNow()
			status := http.StatusOK
			if operation == chargingReleaseOperation {
				status = http.StatusNoContent
			}
			require.Equal(t, status, first.Code, first.Body.String())
			require.Equal(t, 1, calls)
			if tc.requesting {
				var decoded models.Chf_ConvCharging_ChargingDataResponse
				require.NoError(t, json.Unmarshal(first.Body.Bytes(), &decoded))
				require.Equal(t, int32(100), decoded.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
				require.Equal(t, int64(100), ue.ReservedQuota[10])
			}
			// Discard the first response and remove all live session information.
			chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
			p = &Processor{journal: charging.NewJournal(db)}
			request.RetransmissionIndicator = true
			replay := httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(replay)
			if operation == chargingUpdateOperation {
				p.HandleChargingdataUpdate(ctx, request, ref)
			} else {
				p.HandleChargingdataRelease(ctx, request, ref)
			}
			ctx.Writer.WriteHeaderNow()
			require.Equal(t, status, replay.Code)
			require.Equal(t, first.Body.String(), replay.Body.String())
			var balance struct {
				Quota string `bson:"quota"`
			}
			require.NoError(t, db.Collection("policyData.ues.chargingData").FindOne(context.Background(),
				bson.M{"ueId": request.SubscriberIdentifier, "ratingGroup": 10}).Decode(&balance))
			require.Equal(t, tc.remaining, balance.Quota)
		})
	}
}

func TestMongoChargingAccountCommittedButCHFUnfinishedRemainsFenced(t *testing.T) {
	db := processorMongo(t)
	p, request, ref := chargingRequestSession(t)
	p.journal = charging.NewJournal(db)
	chf_context.GetSelf().AbmfCfg.OriginHost = "chf-test"
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
		"ueId": request.SubscriberIdentifier, "ratingGroup": 10, "quota": "100",
	})
	require.NoError(t, err)
	accounts := charging.NewAccounts(db)
	var captured *charging_datatype.AccountDebitRequest
	p.accountDebit = func(_ *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		copyRequest := *account
		captured = &copyRequest
		_, applyErr := accounts.Apply(context.Background(), account)
		if applyErr != nil {
			return nil, applyErr
		}
		return nil, errors.New("ABMF answer lost after committing debit")
	}
	p.serviceUsage = func(_ *chf_context.ChfUe, _ *charging_datatype.ServiceUsageRequest) (
		*charging_datatype.ServiceUsageResponse, error,
	) {
		return &charging_datatype.ServiceUsageResponse{ServiceRating: &charging_datatype.ServiceRating{Price: 40}}, nil
	}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
	require.NotNil(t, captured)
	// ABMF independently knows the operation succeeded, and can safely replay it.
	replayed, err := charging.NewAccounts(db).Apply(context.Background(), captured)
	require.NoError(t, err)
	require.Equal(t, datatype.Unsigned32(0), replayed.CcRequestNumber)
	chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
	p = &Processor{journal: charging.NewJournal(db)}
	_, problem = p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
	request.InvocationSequenceNumber++
	require.NotNil(t, p.ChargingDataRelease(request, ref))
	var balance struct {
		Quota string `bson:"quota"`
	}
	require.NoError(t, db.Collection("policyData.ues.chargingData").FindOne(context.Background(),
		bson.M{"ueId": request.SubscriberIdentifier, "ratingGroup": 10}).Decode(&balance))
	require.Equal(t, "60", balance.Quota)
}
