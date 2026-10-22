package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/testutil"
	"github.com/free5gc/openapi/models"
)

// The API methods, reservation logic and HTTP handlers are the production
// implementations; only journal storage and external account/rating calls are stubbed.
type testJournal struct {
	mu        sync.Mutex
	records   map[string]*charging.Record
	active    map[string]string
	finishErr error
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
	record := &charging.Record{ID: identity.ID(), Identity: identity, State: charging.Processing}
	j.records[record.ID] = record
	j.active[record.Session] = record.ID
	copyRecord := *record
	return &copyRecord, true, nil
}

func (j *testJournal) Finish(_ context.Context, record *charging.Record, result charging.Result, state string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finishErr != nil {
		return j.finishErr
	}
	j.records[record.ID].State = state
	j.records[record.ID].Result = result
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

func chargingRequestSession(t *testing.T) (*Processor, models.Chf_ConvCharging_ChargingDataRequest, string) {
	t.Helper()
	const supi, ref = "imsi-208930000001226", "charging-1226"
	self := chf_context.GetSelf()
	previousAbmf, previousRating := self.AbmfCfg, self.RatingCfg
	self.AbmfCfg, self.RatingCfg = &sm.Settings{}, &sm.Settings{}
	ue := &chf_context.ChfUe{
		Supi: supi, Cdr: make(map[string]*cdrType.CHFRecord),
		ReservedQuota: map[int32]int64{10: 0}, RatingType: make(map[int32]charging_datatype.RequestSubType),
		UnitCost: make(map[int32]uint32), AcctRequestNum: make(map[int32]uint32),
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
	p := &Processor{journal: newTestJournal()}
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

func TestChargingRequestReplaysResultAfterContextRemoval(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			calls := countAccountCalls(p)
			first := chargingAPIResponse(t, p, request, ref, operation)
			status := http.StatusOK
			if operation == chargingReleaseOperation {
				status = http.StatusNoContent
			}
			require.Equal(t, status, first.Code, first.Body.String())
			chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
			// Recreate the processor: replay cannot rely on a live UE/CDR or account client.
			p = &Processor{journal: p.journal}
			request.RetransmissionIndicator = true
			replayed := chargingAPIResponse(t, p, request, ref, operation)
			require.Equal(t, status, replayed.Code)
			require.Equal(t, first.Body.String(), replayed.Body.String())
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestChargingRequestRejectsChangedContentAndOperation(t *testing.T) {
	for _, change := range []string{"usage", "subscriber", "operation"} {
		t.Run(change, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			calls := countAccountCalls(p)
			_, problem := p.ChargingDataUpdate(request, ref)
			require.Nil(t, problem)
			if change == "usage" {
				request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume++
			}
			if change == "subscriber" {
				request.SubscriberIdentifier = "imsi-other"
			}
			if change == "operation" {
				problem = p.ChargingDataRelease(request, ref)
			} else {
				_, problem = p.ChargingDataUpdate(request, ref)
			}
			require.NotNil(t, problem)
			require.Equal(t, int32(http.StatusConflict), problem.Status)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestChargingRequestInProgressBlocksConcurrentOperations(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	started, proceed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	allow := sync.OnceFunc(func() { close(proceed) })
	t.Cleanup(func() { allow(); <-done })
	original := p.accountDebit
	var calls atomic.Int32
	p.accountDebit = func(ue *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		calls.Add(1)
		close(started)
		<-proceed
		return original(ue, account)
	}
	go func() {
		defer close(done)
		_, problem := p.ChargingDataUpdate(request, ref)
		if problem != nil {
			t.Errorf("original request failed: %+v", problem)
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("update did not reach accounting")
	}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
	request.InvocationSequenceNumber++
	require.NotNil(t, p.ChargingDataRelease(request, ref))
	require.Equal(t, int32(1), calls.Load())
	allow()
	<-done
}

func TestChargingAccountFailureIsNotAcknowledgedOrRepeated(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			calls := 0
			p.accountDebit = func(_ *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
				*charging_datatype.AccountDebitResponse, error,
			) {
				calls++
				require.NotEmpty(t, account.SessionId)
				require.Equal(t, datatype.Unsigned64(40), account.MultipleServicesCreditControl.UsedServiceUnit.CCTotalOctets)
				return nil, errors.New("lost answer after debit")
			}
			for attempt := 0; attempt < 2; attempt++ {
				reply := chargingAPIResponse(t, p, request, ref, operation)
				require.Equal(t, http.StatusServiceUnavailable, reply.Code)
			}
			request.InvocationSequenceNumber++
			require.NotNil(t, p.ChargingDataRelease(request, ref))
			require.Equal(t, 1, calls)
		})
	}
}

func TestChargingCompletionPersistenceFailureBlocksSuccess(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			p.journal.(*testJournal).finishErr = errors.New("database unavailable after accounting")
			calls := countAccountCalls(p)
			for attempt := 0; attempt < 2; attempt++ {
				reply := chargingAPIResponse(t, p, request, ref, operation)
				require.Equal(t, http.StatusServiceUnavailable, reply.Code)
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestChargingWithoutJournalDoesNotExecute(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			p.journal = nil
			calls := countAccountCalls(p)
			reply := chargingAPIResponse(t, p, request, ref, operation)
			require.Equal(t, http.StatusServiceUnavailable, reply.Code)
			require.Zero(t, calls.Load())
		})
	}
}

func TestChargingRequestRejectedBeforeAccountingReplaysRejection(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			calls := countAccountCalls(p)
			chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
			for attempt := 0; attempt < 2; attempt++ {
				reply := chargingAPIResponse(t, p, request, ref, operation)
				require.Equal(t, http.StatusNotFound, reply.Code)
			}
			require.Zero(t, calls.Load())
			record := p.journal.(*testJournal).records[(charging.Identity{Session: ref, Sequence: 7}).ID()]
			require.Equal(t, charging.Rejected, record.State)
		})
	}
}

func TestChargingRequestPersistsBodyForReconciliation(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	request.RetransmissionIndicator = true
	_, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	record := p.journal.(*testJournal).records[(charging.Identity{Session: ref, Sequence: 7}).ID()]
	require.NotEmpty(t, record.Request)
	var persisted models.Chf_ConvCharging_ChargingDataRequest
	require.NoError(t, json.Unmarshal(record.Request, &persisted))
	request.RetransmissionIndicator = false
	require.Equal(t, request, persisted)
}

func TestChargingDataUpdateRejectsUnknownChargingDataRef(t *testing.T) {
	self := chf_context.GetSelf()
	supi := "imsi-208930000000950"

	ue := &chf_context.ChfUe{
		Supi: supi,
		Cdr: map[string]*cdrType.CHFRecord{
			"valid-session": {
				ChargingFunctionRecord: &cdrType.ChargingRecord{},
			},
		},
		Records: []*cdrType.CHFRecord{
			{ChargingFunctionRecord: &cdrType.ChargingRecord{}},
			{ChargingFunctionRecord: &cdrType.ChargingRecord{}},
		},
	}

	self.UePool.Store(supi, ue)
	t.Cleanup(func() {
		self.UePool.Delete(supi)
	})

	p := &Processor{journal: newTestJournal()}
	response, problemDetails := p.ChargingDataUpdate(models.Chf_ConvCharging_ChargingDataRequest{
		SubscriberIdentifier: supi,
	}, "does-not-exist-partial")

	require.Nil(t, response)
	require.NotNil(t, problemDetails)
	require.Equal(t, http.StatusNotFound, int(problemDetails.Status))
	require.Equal(t, "CONTEXT_NOT_FOUND", problemDetails.Cause)
}

// A valid usage report must reach session lookup instead of being rejected for
// omitting RequestedUnit. Use an unknown reference to avoid external rating/CDR I/O.
func TestChargingdataUpdateAcceptsLastOnlineUsageWithoutRequestedUnit(t *testing.T) {
	self := chf_context.GetSelf()
	const supi = "imsi-208930000000952"
	self.UePool.Store(supi, &chf_context.ChfUe{Supi: supi, Cdr: map[string]*cdrType.CHFRecord{}})
	t.Cleanup(func() { self.UePool.Delete(supi) })
	finalTrigger := models.Chf_ConvCharging_Trigger{
		TriggerType:     models.Chf_ConvCharging_TriggerType_FINAL,
		TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
	}
	for _, tc := range []struct {
		name            string
		requestTriggers []models.Chf_ConvCharging_Trigger
		usageTriggers   []models.Chf_ConvCharging_Trigger
		ongoingGroup    bool
	}{
		{name: "implicit last request"},
		{name: "request FINAL", requestTriggers: []models.Chf_ConvCharging_Trigger{finalTrigger}},
		{name: "usage FINAL", usageTriggers: []models.Chf_ConvCharging_Trigger{finalTrigger}},
		{name: "another rating group requests quota", ongoingGroup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := models.Chf_ConvCharging_ChargingDataRequest{
				SubscriberIdentifier: supi,
				Triggers:             tc.requestTriggers,
				MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{{
					RatingGroup: 1,
					UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
						QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
						TotalVolume:              600,
						Triggers:                 tc.usageTriggers,
					}},
				}},
			}
			if tc.ongoingGroup {
				request.MultipleUnitUsage = append(request.MultipleUnitUsage,
					models.Chf_ConvCharging_MultipleUnitUsage{
						RatingGroup:   2,
						RequestedUnit: &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 1000},
						UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
							QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
						}},
					})
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			(&Processor{journal: newTestJournal()}).HandleChargingdataUpdate(ctx, request, "unknown-charging-data-ref")
			require.Equal(t, http.StatusNotFound, recorder.Code)
			require.JSONEq(t, `{
				"title":"Context Not Found", "status":404,
				"detail":"ChargingDataRef[unknown-charging-data-ref] not found", "cause":"CONTEXT_NOT_FOUND"
			}`, recorder.Body.String())
		})
	}
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
			db := testutil.Mongo(t)
			p, request, ref := chargingRequestSession(t)
			p.journal = charging.NewJournal(db)
			initialBalance := "100"
			if tc.requesting {
				initialBalance = "1000"
				request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
			}
			ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			require.True(t, ok)
			ue.ReservedQuota[10] = tc.reserved
			_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
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
			first := chargingAPIResponse(t, p, request, ref, operation)
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
			_, err = db.Collection("policyData.ues.chargingData").DeleteMany(context.Background(), bson.M{})
			require.NoError(t, err)
			// Discard the first response and remove all live session information.
			chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
			p = &Processor{journal: charging.NewJournal(db)}
			request.RetransmissionIndicator = true
			replay := chargingAPIResponse(t, p, request, ref, operation)
			require.Equal(t, status, replay.Code)
			require.Equal(t, first.Body.String(), replay.Body.String())
			var balance struct {
				Quota string `bson:"quota"`
			}
			require.NoError(t, db.Collection("chf.accounts").FindOne(context.Background(),
				bson.M{"ueId": request.SubscriberIdentifier, "ratingGroup": 10}).Decode(&balance))
			require.Equal(t, tc.remaining, balance.Quota)
		})
	}
}

func TestMongoChargingAccountCommittedButCHFUnfinishedRemainsFenced(t *testing.T) {
	db := testutil.Mongo(t)
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
	_, err = db.Collection("policyData.ues.chargingData").DeleteMany(context.Background(), bson.M{})
	require.NoError(t, err)
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
	require.NoError(t, db.Collection("chf.accounts").FindOne(context.Background(),
		bson.M{"ueId": request.SubscriberIdentifier, "ratingGroup": 10}).Decode(&balance))
	require.Equal(t, "60", balance.Quota)
}
