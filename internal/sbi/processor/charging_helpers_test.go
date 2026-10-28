package processor

import (
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/sm"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

const testAccountOriginHost = "chf-test"

func chargingRequestSession(t *testing.T) (*Processor, models.Chf_ConvCharging_ChargingDataRequest, string) {
	t.Helper()
	const supi, ref = "imsi-208930000001226", "charging-1226"
	self := chf_context.GetSelf()
	previousAbmf, previousRating := self.AbmfCfg, self.RatingCfg
	self.AbmfCfg, self.RatingCfg = &sm.Settings{}, &sm.Settings{}
	ue := &chf_context.ChfUe{
		Supi: supi, Cdr: make(map[string]*cdrType.CHFRecord),
		Quotas: make(map[charging.QuotaKey]charging.QuotaState),
	}
	self.UePool.Store(supi, ue)
	t.Cleanup(func() {
		self.UePool.Delete(supi)
		self.AbmfCfg, self.RatingCfg = previousAbmf, previousRating
		if err := os.Remove(buildCdrFileName(supi)); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove test CDR: %v", err)
		}
	})
	request := models.Chf_ConvCharging_ChargingDataRequest{
		SubscriberIdentifier: supi, InvocationSequenceNumber: 7,
		NfConsumerIdentification: &models.Chf_ConvCharging_NFIdentification{
			NodeFunctionality: models.Chf_ConvCharging_NodeFunctionality_SMF, NFName: "smf-test",
		},
		MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{{
			RatingGroup: 10, UPFID: "upf-a",
			UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
				TotalVolume:              40,
				QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
			}},
		}},
	}
	p := &Processor{}
	cdr, err := p.OpenCDR(request, ue, ref, false)
	require.NoError(t, err)
	ue.Cdr[ref] = cdr
	ue.Records = []*cdrType.CHFRecord{cdr}
	p.serviceUsage = func(_ *chf_context.ChfUe, _ *charging_datatype.ServiceUsageRequest) (
		*charging_datatype.ServiceUsageResponse, error,
	) {
		return &charging_datatype.ServiceUsageResponse{ServiceRating: &charging_datatype.ServiceRating{Price: 40}}, nil
	}
	p.accountDebit = func(_ *chf_context.ChfUe, request *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		return &charging_datatype.AccountDebitResponse{
			SessionId: request.SessionId, CcRequestType: request.CcRequestType,
			CcRequestNumber: request.CcRequestNumber, ResultCode: diam.Success,
		}, nil
	}
	return p, request, ref
}

func countAccountCalls(p *Processor) *atomic.Int32 {
	var calls atomic.Int32
	original := p.accountDebit
	p.accountDebit = func(ue *chf_context.ChfUe, request *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		calls.Add(1)
		return original(ue, request)
	}
	return &calls
}

func chargingAPIResponse(
	t *testing.T, p *Processor, request models.Chf_ConvCharging_ChargingDataRequest, ref, operation string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	switch operation {
	case chargingUpdateOperation:
		p.HandleChargingdataUpdate(ctx, request, ref)
	case chargingReleaseOperation:
		p.HandleChargingdataRelease(ctx, request, ref)
	default:
		t.Fatalf("unknown operation %s", operation)
	}
	ctx.Writer.WriteHeaderNow()
	return recorder
}

func onlineQuotaUsage(upf string, used int32, requestCredit bool) models.Chf_ConvCharging_MultipleUnitUsage {
	usage := models.Chf_ConvCharging_MultipleUnitUsage{
		RatingGroup: 10, UPFID: upf,
		UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
			TotalVolume:              used,
			QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
		}},
	}
	if requestCredit {
		usage.RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	}
	return usage
}

func fixedQuotaRating(cost uint32) serviceUsageFunc {
	return func(_ *chf_context.ChfUe, request *charging_datatype.ServiceUsageRequest) (
		*charging_datatype.ServiceUsageResponse, error,
	) {
		rating := &charging_datatype.ServiceRating{
			MonetaryTariff: &charging_datatype.MonetaryTariff{RateElement: &charging_datatype.RateElement{
				UnitCost: &charging_datatype.UnitCost{ValueDigits: datatype.Integer64(cost)},
			}},
		}
		if request.ServiceRating.RequestSubType == charging_datatype.REQ_SUBTYPE_DEBIT {
			rating.Price = request.ServiceRating.ConsumedUnits * datatype.Unsigned32(cost)
		} else {
			rating.AllowedUnits = request.ServiceRating.MonetaryQuota / datatype.Unsigned32(cost)
		}
		return &charging_datatype.ServiceUsageResponse{ServiceRating: rating}, nil
	}
}

func grantQuotaRequest(_ *chf_context.ChfUe, request *charging_datatype.AccountDebitRequest) (
	*charging_datatype.AccountDebitResponse, error,
) {
	response := &charging_datatype.AccountDebitResponse{
		SessionId: request.SessionId, CcRequestNumber: request.CcRequestNumber,
		CcRequestType: request.CcRequestType, ResultCode: diam.Success,
	}
	if request.RequestedAction == charging_datatype.DIRECT_DEBITING &&
		request.MultipleServicesCreditControl.RequestedServiceUnit != nil {
		response.MultipleServicesCreditControl = &charging_datatype.MultipleServicesCreditControl{
			GrantedServiceUnit: &charging_datatype.GrantedServiceUnit{
				CCTotalOctets: request.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets,
			},
		}
	}
	return response, nil
}

func useMongoQuotaAccount(
	t *testing.T, p *Processor, db *mongo.Database, subscriber string, balance string,
) {
	t.Helper()
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
		"ueId": subscriber, "ratingGroup": 10, "quota": balance,
	})
	require.NoError(t, err)
	accounts := charging.NewAccounts(db)
	p.accountDebit = func(_ *chf_context.ChfUe, request *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		return accounts.Apply(context.Background(), request)
	}
	chf_context.GetSelf().AbmfCfg.OriginHost = testAccountOriginHost
}

func requireQuotaBalance(t *testing.T, db *mongo.Database, subscriber string, expected int64) {
	t.Helper()
	var balance struct {
		Quota string `bson:"quota"`
	}
	require.NoError(t, db.Collection("policyData.ues.chargingData").FindOne(context.Background(), bson.M{
		"ueId": subscriber, "ratingGroup": 10,
	}).Decode(&balance))
	require.Equal(t, strconv.FormatInt(expected, 10), balance.Quota)
}
