package processor

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/chf/internal/testutil"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/idgenerator"
)

const (
	recoveryUnprepared = "unprepared"
	recoveryUnplanned  = "unplanned"
	recoveryDependent  = "dependent"
	recoveryQuota      = "quota"
)

type recoveryFaultJournal struct {
	charging.RecoveryJournal
	point string
}

func (j *recoveryFaultJournal) SaveSettlementOutcome(
	ctx context.Context, record *charging.Record, index int, outcome *charging.SettlementOutcome,
) error {
	if j.point == "outcome" {
		return errors.New("outcome storage unavailable")
	}
	return j.RecoveryJournal.SaveSettlementOutcome(ctx, record, index, outcome)
}

func (j *recoveryFaultJournal) ApplyQuota(
	ctx context.Context, record *charging.Record, index int,
) (charging.QuotaState, error) {
	state, err := j.RecoveryJournal.ApplyQuota(ctx, record, index)
	if err == nil && j.point == recoveryQuota {
		return charging.QuotaState{}, errors.New("quota acknowledgement lost")
	}
	return state, err
}

func (j *recoveryFaultJournal) Finish(
	ctx context.Context, record *charging.Record, result charging.Result, state string,
) error {
	if j.point == "finish" || j.point == recoveryDependent {
		return errors.New("completion storage unavailable")
	}
	return j.RecoveryJournal.Finish(ctx, record, result, state)
}

func makeRecoveryReady(t *testing.T, db *mongo.Database, ref string) {
	t.Helper()
	past := time.Now().Add(-time.Minute)
	_, err := db.Collection("chf.chargingSessions").UpdateOne(context.Background(), bson.M{"_id": ref},
		bson.M{"$set": bson.M{"leaseUntil": past, "readyAt": past}})
	require.NoError(t, err)
	_, err = db.Collection("chf.chargingRequests").UpdateMany(context.Background(), bson.M{
		"session": ref, "state": bson.M{"$in": bson.A{charging.Processing, charging.Uncertain}},
	},
		bson.M{"$set": bson.M{"leaseUntil": past}})
	require.NoError(t, err)
}

func initializeRecoveryClients(t *testing.T) {
	t.Helper()
	self := chf_context.GetSelf()
	config, ratings, accounts := factory.ChfConfig, self.RatingSessionIdGenerator, self.AccountSessionIdGenerator
	factory.ChfConfig = &factory.Config{Configuration: &factory.Configuration{VolumeThresholdRate: 0.8}}
	self.RatingSessionIdGenerator = idgenerator.NewGenerator(1, math.MaxUint32)
	self.AccountSessionIdGenerator = idgenerator.NewGenerator(1, math.MaxUint32)
	t.Cleanup(func() {
		factory.ChfConfig = config
		self.RatingSessionIdGenerator, self.AccountSessionIdGenerator = ratings, accounts
	})
}

func TestMongoBackgroundRecoveryResumesFrozenProgressAfterRestart(t *testing.T) {
	for _, phase := range []string{
		"account", "outcome", recoveryQuota, "cdr", "finish", recoveryUnprepared, recoveryUnplanned, recoveryDependent,
		chargingReleaseOperation,
	} {
		t.Run(phase, func(t *testing.T) {
			db := testutil.Mongo(t)
			p, request, ref := chargingRequestSession(t)
			initializeRecoveryClients(t)
			p.serviceUsage = fixedQuotaRating(1)
			useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
			require.NoError(t, charging.NewAccounts(db).ImportLegacy(context.Background()))
			ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			initializeTestCDR(t, p, ue, ref)
			request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
			if phase == recoveryQuota {
				request.Triggers = []models.Chf_ConvCharging_Trigger{{
					TriggerType:     models.Chf_ConvCharging_TriggerType_VOLUME_LIMIT,
					TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
				}}
			}
			if phase == recoveryDependent {
				request.MultipleUnitUsage = append(request.MultipleUnitUsage, onlineQuotaUsage("upf-a", 30, true))
			}
			originalAccount := p.accountDebit
			if phase == "account" {
				p.accountDebit = func(ue *chf_context.ChfUe, req *charging_datatype.AccountDebitRequest) (
					*charging_datatype.AccountDebitResponse, error,
				) {
					_, err := originalAccount(ue, req)
					require.NoError(t, err)
					return nil, errors.New("account answer lost after commit")
				}
			}
			journal := charging.NewJournal(db)
			p.journal = &recoveryFaultJournal{RecoveryJournal: journal, point: phase}
			if phase == "cdr" || phase == chargingReleaseOperation {
				p.cdrWriter = func(string, []*cdrType.CHFRecord) error { return errors.New("disk unavailable") }
			}
			if phase == recoveryUnprepared {
				request.MultipleUnitUsage = append(request.MultipleUnitUsage, onlineQuotaUsage("upf-b", 20, true))
				rate := p.serviceUsage
				calls := 0
				p.serviceUsage = func(ue *chf_context.ChfUe, req *charging_datatype.ServiceUsageRequest) (
					*charging_datatype.ServiceUsageResponse, error,
				) {
					calls++
					if calls == 4 {
						return nil, errors.New("RF unavailable for later entry")
					}
					return rate(ue, req)
				}
			}
			switch phase {
			case recoveryUnplanned:
				_, problem := p.beginChargingRequest(request, ref, chargingUpdateOperation)
				require.Nil(t, problem)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 1000)
			case chargingReleaseOperation:
				request.MultipleUnitUsage[0].RequestedUnit = nil
				require.NotNil(t, p.ChargingDataRelease(request, ref))
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 960)
			default:
				_, problem := p.ChargingDataUpdate(request, ref)
				require.NotNil(t, problem)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 860)
			}
			makeRecoveryReady(t, db, ref)
			chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
			// A new processor has no live quota/CDR projection or old transport objects.
			restarted := &Processor{
				journal: charging.NewJournal(db), accountDebit: originalAccount,
				bindAccount: charging.NewAccounts(db).Bind,
			}
			var ratings atomic.Int32
			restarted.serviceUsage = func(ue *chf_context.ChfUe, req *charging_datatype.ServiceUsageRequest) (
				*charging_datatype.ServiceUsageResponse, error,
			) {
				ratings.Add(1)
				if phase != recoveryUnprepared && phase != recoveryUnplanned {
					return nil, errors.New("saved steps must not be rated again")
				}
				return fixedQuotaRating(1)(ue, req)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); restarted.RunChargingRecovery(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			id := (charging.Identity{Session: ref, Sequence: request.InvocationSequenceNumber}).ID()
			require.Eventually(t, func() bool {
				var record charging.Record
				err := db.Collection("chf.chargingRequests").FindOne(context.Background(), bson.M{"_id": id}).Decode(&record)
				return err == nil && record.State == charging.Succeeded
			}, 5*time.Second, 10*time.Millisecond)
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("recovery worker failed to stop")
			}
			var session struct {
				Active *string `bson:"active"`
			}
			require.NoError(t, db.Collection("chf.chargingSessions").
				FindOne(context.Background(), bson.M{"_id": ref}).Decode(&session))
			require.Nil(t, session.Active)
			restored, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			require.True(t, ok)
			require.NotNil(t, restored.AbmfClient)
			require.NotNil(t, restored.RatingClient)
			expectedVolume, expectedEntries, balance := int64(40), 1, int64(860)
			if phase == recoveryUnprepared {
				expectedVolume, expectedEntries, balance = 60, 2, 740
			}
			if phase == recoveryDependent {
				expectedVolume, expectedEntries = 70, 2
			}
			if phase == chargingReleaseOperation {
				balance = 960
			}
			restored.CULock.Lock()
			volume, entries := cdrUsageTotals(restored.Records)
			closed := restored.CDRProgress[ref].Closed
			restored.CULock.Unlock()
			require.Equal(t, expectedVolume, volume)
			require.Equal(t, expectedEntries, entries)
			require.Equal(t, phase == chargingReleaseOperation, closed)
			if phase == recoveryQuota {
				require.Len(t, restored.Records, 2, "partial boundary survives resumption")
			}
			requireQuotaBalance(t, db, request.SubscriberIdentifier, balance)
			if phase == recoveryUnprepared || phase == recoveryUnplanned {
				require.Equal(t, int32(3), ratings.Load())
			} else {
				require.Zero(t, ratings.Load())
			}
			if phase == chargingReleaseOperation {
				require.Nil(t, restarted.ChargingDataRelease(request, ref))
			} else {
				response, problem := restarted.ChargingDataUpdate(request, ref)
				require.Nil(t, problem)
				require.Equal(t, request.InvocationSequenceNumber, response.InvocationSequenceNumber)
				require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
			}
			requireQuotaBalance(t, db, request.SubscriberIdentifier, balance)
		})
	}
}

func TestMongoRecoveryRefreshesStaleProjectionBeforeNextInvocation(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			db := testutil.Mongo(t)
			p, request, ref := chargingRequestSession(t)
			initializeRecoveryClients(t)
			p.serviceUsage = fixedQuotaRating(1)
			useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
			calls := countAccountCalls(p)
			original, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			initializeTestCDR(t, p, original, ref)
			request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
			_, problem := p.ChargingDataUpdate(request, ref)
			require.Nil(t, problem)
			prefix := original.CDRProgress[ref].EventID
			request.InvocationSequenceNumber++
			request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume = 30
			if operation == chargingReleaseOperation {
				request.MultipleUnitUsage[0].RequestedUnit = nil
			}
			p.journal = &recoveryFaultJournal{RecoveryJournal: charging.NewJournal(db), point: "outcome"}
			reply := chargingAPIResponse(t, p, request, ref, operation)
			require.Equal(t, 503, reply.Code)
			require.Equal(t, prefix, original.CDRProgress[ref].EventID)
			makeRecoveryReady(t, db, ref)
			journal := charging.NewJournal(db)
			restarted := &Processor{
				journal: journal, accountDebit: p.accountDebit, serviceUsage: p.serviceUsage, bindAccount: p.bindAccount,
			}
			record, err := journal.ClaimRecovery(context.Background())
			require.NoError(t, err)
			require.NotNil(t, record)
			restarted.cdrWriter = func(string, []*cdrType.CHFRecord) error { return errors.New("disk still unavailable") }
			restarted.recoverChargingRequest(record)
			var pending charging.Record
			require.NoError(t, db.Collection("chf.chargingRequests").FindOne(
				context.Background(), bson.M{"_id": record.ID}).Decode(&pending))
			require.Equal(t, charging.Uncertain, pending.State)
			next, err := journal.ClaimRecovery(context.Background())
			require.NoError(t, err)
			require.Nil(t, next, "the recovery loop must respect backoff")
			// Model another CHF instance with an empty in-memory projection.
			chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
			makeRecoveryReady(t, db, ref)
			next, err = journal.ClaimRecovery(context.Background())
			require.NoError(t, err)
			require.NotNil(t, next)
			restarted.cdrWriter = nil
			require.NoError(t, restarted.resumeChargingRequest(next))
			chf_context.GetSelf().UePool.Store(request.SubscriberIdentifier, original)
			// The old instance still has only the first event. Its existing quota read
			// detects cdrHead and rebuilds before granting or rejecting the next request.
			p.journal = charging.NewJournal(db)
			before := calls.Load()
			request.InvocationSequenceNumber++
			request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
			request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume = 20
			response, problem := p.ChargingDataUpdate(request, ref)
			if operation == chargingReleaseOperation {
				require.NotNil(t, problem)
				require.Equal(t, int32(404), problem.Status)
				require.Nil(t, response)
				require.True(t, original.CDRProgress[ref].Closed)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 930)
			} else {
				require.Nil(t, problem)
				require.Equal(t, int32(50), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 860)
			}
			require.Equal(t, before, calls.Load(), "stale CDR state cannot cause another account operation")
			restored := &chf_context.ChfUe{Supi: request.SubscriberIdentifier}
			require.NoError(t, p.RestoreCDRs(restored))
			volume, entries := cdrUsageTotals(restored.Records)
			if operation == chargingReleaseOperation {
				require.Equal(t, int64(70), volume)
				require.Equal(t, 2, entries)
			} else {
				require.Equal(t, int64(90), volume)
				require.Equal(t, 3, entries)
			}
		})
	}
}

func TestMongoRecoveryCompletesInterruptedValidationRejection(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ambiguous ownership", true: "released resource"}[closed], func(t *testing.T) {
			db := testutil.Mongo(t)
			p, request, ref := chargingRequestSession(t)
			p.serviceUsage = fixedQuotaRating(1)
			useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
			ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			initializeTestCDR(t, p, ue, ref)
			status := int32(400)
			if closed {
				require.Nil(t, p.ChargingDataRelease(request, ref))
				request.InvocationSequenceNumber++
				status = 404
			} else {
				request.MultipleUnitUsage = append(request.MultipleUnitUsage, onlineQuotaUsage("", 0, true))
			}
			calls := countAccountCalls(p)
			p.journal = &recoveryFaultJournal{RecoveryJournal: charging.NewJournal(db), point: "finish"}
			_, problem := p.ChargingDataUpdate(request, ref)
			require.NotNil(t, problem)
			require.Equal(t, int32(503), problem.Status)
			require.Zero(t, calls.Load())
			makeRecoveryReady(t, db, ref)
			journal := charging.NewJournal(db)
			record, err := journal.ClaimRecovery(context.Background())
			require.NoError(t, err)
			require.NotNil(t, record)
			p.journal = journal
			require.NoError(t, p.resumeChargingRequest(record))
			_, problem = p.ChargingDataUpdate(request, ref)
			require.NotNil(t, problem)
			require.Equal(t, status, problem.Status)
			require.Zero(t, calls.Load(), "validation recovery cannot start accounting")
			var session struct {
				Active *string `bson:"active"`
			}
			require.NoError(t, db.Collection("chf.chargingSessions").FindOne(
				context.Background(), bson.M{"_id": ref}).Decode(&session))
			require.Nil(t, session.Active)
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
			key := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
			ue.Quotas[key] = charging.QuotaState{
				Known: true, Reserved: tc.reserved, RatingType: charging_datatype.REQ_SUBTYPE_RESERVE,
			}
			quotaID, err := charging.Digest(key)
			require.NoError(t, err)
			_, err = db.Collection("chf.chargingSessions").InsertOne(context.Background(), bson.M{
				"_id": ref, "active": nil, "quotas": bson.M{quotaID: bson.D{
					{Key: "key", Value: key}, {Key: "state", Value: ue.Quotas[key]},
				}},
			})
			require.NoError(t, err)
			_, err = db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
				"ueId": request.SubscriberIdentifier, "ratingGroup": 10, "quota": initialBalance,
			})
			require.NoError(t, err)
			accounts := charging.NewAccounts(db)
			p.bindAccount = accounts.Bind
			chf_context.GetSelf().AbmfCfg.OriginHost = testAccountOriginHost
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
				require.Equal(t, int64(100), ue.Quotas[key].Reserved)
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
	chf_context.GetSelf().AbmfCfg.OriginHost = testAccountOriginHost
	_, err := db.Collection("policyData.ues.chargingData").InsertOne(context.Background(), bson.M{
		"ueId": request.SubscriberIdentifier, "ratingGroup": 10, "quota": "100",
	})
	require.NoError(t, err)
	accounts := charging.NewAccounts(db)
	p.bindAccount = accounts.Bind
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
	var stored charging.Record
	require.NoError(t, db.Collection("chf.chargingRequests").FindOne(context.Background(), bson.M{
		"_id": (charging.Identity{Session: ref, Sequence: 7}).ID(),
	}).Decode(&stored))
	require.Equal(t, charging.Uncertain, stored.State)
	var saved charging.SettlementStep
	require.NoError(t, chargingdb.DecodeSnapshot(stored.SettlementPlan.Steps["0"], &saved))
	require.Equal(t, *captured, *saved.AccountRequest)
	require.Equal(t, datatype.Unsigned32(40), saved.Ratings[0].Response.ServiceRating.Price)
	require.Empty(t, stored.SettlementPlan.Outcomes)
	_, err = db.Collection("policyData.ues.chargingData").DeleteMany(context.Background(), bson.M{})
	require.NoError(t, err)
	// ABMF independently knows the operation succeeded, and can safely replay it.
	replayed, err := charging.NewAccounts(db).Apply(context.Background(), saved.AccountRequest)
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
