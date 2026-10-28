package processor

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/openapi/models"
)

const testAccountOriginHost = "chf-test"

// The API methods, reservation logic and HTTP handlers are the production
// implementations; only journal storage and external account/rating calls are stubbed.
type testJournal struct {
	mu         sync.Mutex
	records    map[string]*charging.Record
	active     map[string]string
	stepErr    error
	outcomeErr error
	quotaErr   error
	loadErr    error
	quotas     map[charging.QuotaKey]charging.QuotaState
}

func newTestJournal() *testJournal {
	return &testJournal{records: make(map[string]*charging.Record), active: make(map[string]string)}
}

func (j *testJournal) Begin(_ context.Context, identity charging.Identity) (*charging.Record, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if record := j.records[identity.ID()]; record != nil {
		if record.Fingerprint != identity.Fingerprint || record.Operation != identity.Operation {
			return nil, false, charging.ErrConflict
		}
		if record.State != charging.Succeeded && record.State != charging.Rejected {
			return nil, false, charging.ErrPending
		}
		copyRecord := *record
		return &copyRecord, false, nil
	}
	if j.active[identity.Session] != "" {
		return nil, false, charging.ErrPending
	}
	record := &charging.Record{
		ID: identity.ID(), Identity: identity, State: charging.Processing, CreatedAt: time.Now().UTC(),
	}
	j.records[record.ID] = record
	j.active[record.Session] = record.ID
	copyRecord := *record
	return &copyRecord, true, nil
}

func (j *testJournal) Finish(_ context.Context, record *charging.Record, result charging.Result, state string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records[record.ID].State = state
	j.records[record.ID].Result = result
	if state == charging.Succeeded {
		j.records[record.ID].CDR = record.CDR
	}
	delete(j.active, record.Session)
	return nil
}

func (j *testJournal) MarkUncertain(_ context.Context, record *charging.Record, detail string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records[record.ID].State = charging.Uncertain
	j.records[record.ID].Detail = detail
	return nil
}

func (j *testJournal) SaveSettlementStep(
	_ context.Context, record *charging.Record, step *charging.SettlementStep,
) error {
	return j.saveSnapshot(record, step.UsageIndex, step, false)
}

func (j *testJournal) SaveSettlementOutcome(
	_ context.Context, record *charging.Record, index int, outcome *charging.SettlementOutcome,
) error {
	return j.saveSnapshot(record, index, outcome, true)
}

func (j *testJournal) saveSnapshot(record *charging.Record, index int, value any, outcome bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !outcome && j.stepErr != nil {
		return j.stepErr
	}
	if outcome && j.outcomeErr != nil {
		return j.outcomeErr
	}
	stored := j.records[record.ID]
	if stored == nil || stored.State != charging.Processing {
		return charging.ErrPending
	}
	if stored.SettlementPlan == nil {
		stored.SettlementPlan = &charging.SettlementPlan{
			Steps: make(map[string][]byte), Outcomes: make(map[string][]byte),
		}
	}
	values := stored.SettlementPlan.Steps
	key := strconv.Itoa(index)
	if outcome {
		if values[key] == nil {
			return charging.ErrConflict
		}
		values = stored.SettlementPlan.Outcomes
	}
	body, err := chargingdb.EncodeSnapshot(value)
	if err != nil {
		return err
	}
	if previous := values[key]; previous != nil && !bytes.Equal(previous, body) {
		return charging.ErrConflict
	}
	values[key] = body
	return nil
}

func (j *testJournal) PrepareQuotas(_ context.Context, record *charging.Record) (
	map[charging.QuotaKey]charging.QuotaState, error,
) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.loadErr != nil {
		return nil, j.loadErr
	}
	states := make(map[charging.QuotaKey]charging.QuotaState)
	for key, state := range j.quotas {
		if key.ChargingDataRef == record.Session {
			states[key] = state
		}
	}
	return states, nil
}

func (j *testJournal) ApplyQuota(_ context.Context, record *charging.Record, index int) (charging.QuotaState, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.quotaErr != nil {
		return charging.QuotaState{}, j.quotaErr
	}
	var outcome charging.SettlementOutcome
	body := j.records[record.ID].SettlementPlan.Outcomes[strconv.Itoa(index)]
	if err := chargingdb.DecodeSnapshot(body, &outcome); err != nil {
		return charging.QuotaState{}, err
	}
	j.quotas[outcome.QuotaKey] = outcome.After
	return outcome.After, nil
}

func (j *testJournal) QueueRecharge(_ context.Context, ref string, rg int32) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for key, state := range j.quotas {
		if key.ChargingDataRef == ref && key.RatingGroup == rg && !state.Closed {
			state.Exhausted = false
			state.RatingType = charging_datatype.REQ_SUBTYPE_RESERVE
			j.quotas[key] = state
		}
	}
	return nil
}

func (j *testJournal) InitializeCDR(context.Context, string, *charging.CDRState) error {
	return nil
}

func (j *testJournal) CDRExists(context.Context, string, string) (bool, error) {
	return false, nil
}

func (j *testJournal) LoadCDRHistory(context.Context, string) ([]charging.CDRHistory, error) {
	// This stub has no durable CDR seeds; reconstruction is covered with MongoDB.
	return nil, nil
}

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
	journal := newTestJournal()
	journal.quotas = ue.Quotas
	p := &Processor{journal: journal}
	p.bindAccount = func(context.Context, *charging_datatype.AccountDebitRequest) ([]byte, error) {
		return bson.Marshal(bson.M{"_id": "test-account"})
	}
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
	require.NoError(t, chargingdb.InitializeSchema(context.Background(), db))
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
		"ueId": subscriber, "ratingGroup": 10, "quota": balance,
	})
	require.NoError(t, err)
	accounts := charging.NewAccounts(db)
	p.journal = charging.NewJournal(db)
	p.bindAccount = accounts.Bind
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
	require.NoError(t, db.Collection("chf.accounts").FindOne(context.Background(), bson.M{
		"ueId": subscriber, "ratingGroup": 10,
	}).Decode(&balance))
	require.Equal(t, strconv.FormatInt(expected, 10), balance.Quota)
}
