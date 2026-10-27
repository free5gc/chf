package processor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/testutil"
	"github.com/free5gc/openapi/models"
)

func createRestartSession(t *testing.T, p *Processor, request models.Chf_ConvCharging_ChargingDataRequest) string {
	t.Helper()
	ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	ue.Cdr, ue.Records = make(map[string]*cdrType.CHFRecord), nil
	request.MultipleUnitUsage = nil
	_, location, problem := p.ChargingDataCreate(request)
	require.Nil(t, problem)
	return location[strings.LastIndex(location, "/")+1:]
}

func TestMongoChargingNormalRequestRestoresResourceAfterRestart(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		for _, cache := range []string{"missing UE", "missing resource"} {
			for _, priorUpdate := range []bool{false, true} {
				name := operation + "/" + cache + "/create only"
				if priorUpdate {
					name = operation + "/" + cache + "/completed update"
				}
				t.Run(name, func(t *testing.T) {
					db := testutil.Mongo(t)
					p, request, _ := chargingRequestSession(t)
					initializeRecoveryClients(t)
					p.serviceUsage = fixedQuotaRating(1)
					useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
					ref := createRestartSession(t, p, request)
					request.InvocationSequenceNumber++
					volume, entries, balance := int64(20), 1, int64(980)
					if priorUpdate {
						request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
							onlineQuotaUsage("upf-a", 40, true),
						}
						_, problem := p.ChargingDataUpdate(request, ref)
						require.Nil(t, problem)
						requireQuotaBalance(t, db, request.SubscriberIdentifier, 860)
						request.InvocationSequenceNumber++
						volume, entries, balance = 60, 2, 940
					}
					// Every invocation has completed: recovery has nothing to prewarm.
					var session struct{ Active *string }
					require.NoError(t, db.Collection("chf.chargingSessions").FindOne(
						context.Background(), bson.M{"_id": ref}).Decode(&session))
					require.Nil(t, session.Active)
					self := chf_context.GetSelf()
					self.UePool.Delete(request.SubscriberIdentifier)
					if cache == "missing resource" {
						_, err := self.NewCHFUe(request.SubscriberIdentifier)
						require.NoError(t, err)
					}
					restarted := &Processor{
						journal: charging.NewJournal(db), serviceUsage: p.serviceUsage,
						accountDebit: p.accountDebit, bindAccount: p.bindAccount,
					}
					calls := countAccountCalls(restarted)
					request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
						onlineQuotaUsage("upf-a", 20, false),
					}
					status := http.StatusOK
					if operation == chargingReleaseOperation {
						status = http.StatusNoContent
					}
					first := chargingAPIResponse(t, restarted, request, ref, operation)
					require.Equal(t, status, first.Code, first.Body.String())
					require.Equal(t, int32(1), calls.Load())
					requireQuotaBalance(t, db, request.SubscriberIdentifier, balance)
					ue, ok := self.ChfUeFindBySupi(request.SubscriberIdentifier)
					require.True(t, ok)
					actualVolume, actualEntries := cdrUsageTotals(ue.Records)
					require.Equal(t, volume, actualVolume)
					require.Equal(t, entries, actualEntries)
					require.Equal(t, operation == chargingReleaseOperation, ue.CDRProgress[ref].Closed)
					request.RetransmissionIndicator = true
					replay := chargingAPIResponse(t, restarted, request, ref, operation)
					require.Equal(t, first.Code, replay.Code)
					require.Equal(t, first.Body.String(), replay.Body.String())
					require.Equal(t, int32(1), calls.Load(), "replay must not settle twice")
					requireQuotaBalance(t, db, request.SubscriberIdentifier, balance)
				})
			}
		}
	}
}

type unavailableHistoryJournal struct {
	charging.RecoveryJournal
	lookup bool
}

func (j *unavailableHistoryJournal) CDRExists(ctx context.Context, ref, subscriber string) (bool, error) {
	if j.lookup {
		return false, errors.New("CDR lookup temporarily unavailable")
	}
	return j.RecoveryJournal.CDRExists(ctx, ref, subscriber)
}

func (j *unavailableHistoryJournal) LoadCDRHistory(context.Context, string) ([]charging.CDRHistory, error) {
	return nil, errors.New("CDR history temporarily unavailable")
}

func TestMongoChargingRestoreFailureRemainsRecoverable(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		for _, lookup := range []bool{false, true} {
			name := operation + "/history read"
			if lookup {
				name = operation + "/resource lookup"
			}
			t.Run(name, func(t *testing.T) {
				db := testutil.Mongo(t)
				p, request, _ := chargingRequestSession(t)
				initializeRecoveryClients(t)
				p.serviceUsage = fixedQuotaRating(1)
				useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
				ref := createRestartSession(t, p, request)
				request.InvocationSequenceNumber++
				chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
				journal := charging.NewJournal(db)
				p.journal = &unavailableHistoryJournal{RecoveryJournal: journal, lookup: lookup}
				calls := countAccountCalls(p)
				first := chargingAPIResponse(t, p, request, ref, operation)
				require.Equal(t, http.StatusServiceUnavailable, first.Code, first.Body.String())
				require.Zero(t, calls.Load())
				var record charging.Record
				id := (charging.Identity{Session: ref, Sequence: request.InvocationSequenceNumber}).ID()
				require.NoError(t, db.Collection("chf.chargingRequests").FindOne(
					context.Background(), bson.M{"_id": id}).Decode(&record))
				require.Equal(t, charging.Uncertain, record.State, "storage errors must not become permanent 404s")
				p.journal = journal
				makeRecoveryReady(t, db, ref)
				claimed, err := journal.ClaimRecovery(context.Background())
				require.NoError(t, err)
				require.NotNil(t, claimed)
				p.recoverChargingRequest(claimed)
				replay := chargingAPIResponse(t, p, request, ref, operation)
				status := http.StatusOK
				if operation == chargingReleaseOperation {
					status = http.StatusNoContent
				}
				require.Equal(t, status, replay.Code, replay.Body.String())
				require.Equal(t, int32(1), calls.Load())
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 960)
			})
		}
	}
}

func TestMongoChargingRestoreRejectsInvalidResource(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		for _, invalid := range []string{"unknown reference", "wrong subscriber", "released resource", "one-time event"} {
			t.Run(operation+"/"+invalid, func(t *testing.T) {
				db := testutil.Mongo(t)
				p, request, _ := chargingRequestSession(t)
				initializeRecoveryClients(t)
				p.serviceUsage = fixedQuotaRating(1)
				useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
				request.OneTimeEvent = invalid == "one-time event"
				ref := createRestartSession(t, p, request)
				request.InvocationSequenceNumber++
				if invalid == "released resource" {
					require.Nil(t, p.ChargingDataRelease(request, ref))
					request.InvocationSequenceNumber++
				}
				self := chf_context.GetSelf()
				self.UePool.Delete(request.SubscriberIdentifier)
				switch invalid {
				case "unknown reference":
					ref = "unknown-charging-data-ref"
				case "wrong subscriber":
					request.SubscriberIdentifier = "imsi-208930000001227"
				}
				calls := countAccountCalls(p)
				for range 2 {
					reply := chargingAPIResponse(t, p, request, ref, operation)
					require.Equal(t, http.StatusNotFound, reply.Code, reply.Body.String())
				}
				require.Zero(t, calls.Load())
				if invalid == "unknown reference" || invalid == "wrong subscriber" {
					_, exists := self.ChfUeFindBySupi(request.SubscriberIdentifier)
					require.False(t, exists, "unverified resources must not allocate a UE")
				}
				var record charging.Record
				id := (charging.Identity{Session: ref, Sequence: request.InvocationSequenceNumber}).ID()
				require.NoError(t, db.Collection("chf.chargingRequests").FindOne(
					context.Background(), bson.M{"_id": id}).Decode(&record))
				require.Equal(t, charging.Rejected, record.State)
			})
		}
	}
}
