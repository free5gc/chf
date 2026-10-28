package processor

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/chf/internal/testutil"
	"github.com/free5gc/openapi/models"
)

func TestMongoChargingUPFFinalDoesNotChangeOtherReservation(t *testing.T) {
	for _, used := range []int32{40, 100, 130} {
		for _, tc := range []struct {
			name           string
			batch, reverse bool
		}{
			{"one request A then B", true, false},
			{"one request B then A", true, true},
			{"separate requests A then B", false, false},
			{"separate requests B then A", false, true},
		} {
			t.Run(strconv.Itoa(int(used))+"/"+tc.name, func(t *testing.T) {
				db := testutil.Mongo(t)
				p, request, ref := chargingRequestSession(t)
				p.serviceUsage = fixedQuotaRating(1)
				useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
				request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
					onlineQuotaUsage("upf-a", 0, true), onlineQuotaUsage("upf-b", 0, true),
				}
				response, problem := p.ChargingDataUpdate(request, ref)
				require.Nil(t, problem)
				for _, info := range response.MultipleUnitInformation {
					require.Equal(t, int32(100), info.GrantedUnit.TotalVolume)
				}
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 800)
				entries := []models.Chf_ConvCharging_MultipleUnitUsage{
					onlineQuotaUsage("upf-a", used, false), onlineQuotaUsage("upf-b", 0, true),
				}
				if tc.reverse {
					entries[0], entries[1] = entries[1], entries[0]
				}
				var finalRequest models.Chf_ConvCharging_ChargingDataRequest
				if tc.batch {
					request.InvocationSequenceNumber++
					request.MultipleUnitUsage = entries
					request.Triggers = []models.Chf_ConvCharging_Trigger{{
						TriggerType:     models.Chf_ConvCharging_TriggerType_FINAL,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
					}}
					finalRequest = request
					_, problem = p.ChargingDataUpdate(request, ref)
					require.Nil(t, problem)
				} else {
					for _, entry := range entries {
						request.InvocationSequenceNumber++
						request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{entry}
						if entry.UPFID == "upf-a" {
							finalRequest = request
						}
						_, problem = p.ChargingDataUpdate(request, ref)
						require.Nil(t, problem)
					}
				}
				ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
				require.True(t, ok)
				a := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
				b := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-b"}
				require.Zero(t, ue.Quotas[a].Reserved)
				require.True(t, ue.Quotas[a].Closed)
				require.Equal(t, charging_datatype.REQ_SUBTYPE_DEBIT, ue.Quotas[a].RatingType)
				require.Equal(t, int64(100), ue.Quotas[b].Reserved)
				require.False(t, ue.Quotas[b].Closed)
				require.Equal(t, charging_datatype.REQ_SUBTYPE_RESERVE, ue.Quotas[b].RatingType)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 900-int64(used))
				// B continues independently, without a request-level FINAL trigger.
				request.InvocationSequenceNumber++
				request.Triggers = nil
				request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-b", 100, true)}
				response, problem = p.ChargingDataUpdate(request, ref)
				require.Nil(t, problem)
				require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
				require.Equal(t, int64(100), ue.Quotas[b].Reserved)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 800-int64(used))
				// A's old final request must replay without refunding or changing B again.
				_, problem = p.ChargingDataUpdate(finalRequest, ref)
				require.Nil(t, problem)
				requireQuotaBalance(t, db, request.SubscriberIdentifier, 800-int64(used))
				require.Equal(t, int64(100), ue.Quotas[b].Reserved)
				var record charging.Record
				require.NoError(t, db.Collection("chf.chargingRequests").FindOne(context.Background(), bson.M{
					"_id": (charging.Identity{Session: ref, Sequence: finalRequest.InvocationSequenceNumber}).ID(),
				}).Decode(&record))
				for index, snapshot := range record.SettlementPlan.Steps {
					var step charging.SettlementStep
					require.NoError(t, chargingdb.DecodeSnapshot(snapshot, &step))
					require.Equal(t, ref, step.QuotaKey.ChargingDataRef)
					require.Equal(t, step.UPFID, step.QuotaKey.UPFID)
					var outcome charging.SettlementOutcome
					require.NoError(t, chargingdb.DecodeSnapshot(record.SettlementPlan.Outcomes[index], &outcome))
					require.Equal(t, step.QuotaKey, outcome.QuotaKey)
				}
			})
		}
	}
}

func TestMongoChargingPartialGrantsAreBackedByEachUPFReservation(t *testing.T) {
	for _, cost := range []uint32{1, 2} {
		t.Run(strconv.Itoa(int(cost)), func(t *testing.T) {
			db := testutil.Mongo(t)
			p, request, ref := chargingRequestSession(t)
			p.serviceUsage = fixedQuotaRating(cost)
			useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "150")
			request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
				onlineQuotaUsage("upf-a", 0, true), onlineQuotaUsage("upf-b", 0, true),
			}
			response, problem := p.ChargingDataUpdate(request, ref)
			require.Nil(t, problem)
			ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			require.True(t, ok)
			var totalReserved int64
			for _, info := range response.MultipleUnitInformation {
				state := ue.Quotas[charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: info.UPFID}]
				totalReserved += state.Reserved
				require.LessOrEqual(t, int64(info.GrantedUnit.TotalVolume)*int64(cost), state.Reserved)
				if state.Exhausted {
					require.Equal(t, charging_datatype.REQ_SUBTYPE_DEBIT, state.RatingType)
					require.Equal(t, models.Chf_ConvCharging_FinalUnitAction_TERMINATE, info.FinalUnitIndication.FinalUnitAction)
				}
			}
			require.Equal(t, int64(150), totalReserved)
			requireQuotaBalance(t, db, request.SubscriberIdentifier, 0)
			if cost == 1 {
				require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
				require.Equal(t, int32(50), response.MultipleUnitInformation[1].GrantedUnit.TotalVolume)
			} else {
				require.Equal(t, int32(75), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
				require.Zero(t, response.MultipleUnitInformation[1].GrantedUnit.TotalVolume)
			}
		})
	}
}

func TestChargingResourcesDoNotShareQuotaOrTariff(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage, p.accountDebit = fixedQuotaRating(1), grantQuotaRequest
	calls := countAccountCalls(p)
	ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	require.True(t, ok)
	otherRef := ref + "-other"
	cdr, err := p.OpenCDR(request, ue, otherRef, false)
	require.NoError(t, err)
	ue.Cdr[otherRef] = cdr
	ue.Records = append(ue.Records, cdr)
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 0, true)}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	p.serviceUsage = fixedQuotaRating(2)
	_, problem = p.ChargingDataUpdate(request, otherRef)
	require.Nil(t, problem)
	require.Equal(t, int32(2), calls.Load())
	a := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
	b := charging.QuotaKey{ChargingDataRef: otherRef, RatingGroup: 10, UPFID: "upf-a"}
	require.Equal(t, uint32(1), ue.Quotas[a].UnitCost)
	require.Equal(t, uint32(2), ue.Quotas[b].UnitCost)
	p.serviceUsage = fixedQuotaRating(1)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 40, false)}
	problem = p.ChargingDataRelease(request, ref)
	require.Nil(t, problem)
	require.True(t, ue.Quotas[a].Closed)
	require.Equal(t, int64(200), ue.Quotas[b].Reserved)
	require.False(t, ue.Quotas[b].Closed)
	require.Equal(t, uint32(2), ue.Quotas[b].UnitCost)
}

func TestChargingExhaustionRechargeAndNewQuotaCycle(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	reservations := 0
	p.accountDebit = func(ue *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		response, err := grantQuotaRequest(ue, account)
		if response.MultipleServicesCreditControl != nil {
			reservations++
			if reservations == 1 {
				response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets = 50
				response.MultipleServicesCreditControl.FinalUnitIndication = &charging_datatype.FinalUnitIndication{
					FinalUnitAction: charging_datatype.TERMINATE,
				}
			}
		}
		return response, err
	}
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 0, true)}
	response, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Equal(t, int32(50), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	require.True(t, ok)
	key := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
	require.True(t, ue.Quotas[key].Exhausted)
	request.InvocationSequenceNumber++
	response, problem = p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Zero(t, response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	require.Equal(t, 1, reservations, "a live exhausted quota cannot reserve again before recharge")
	closedKey := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-closed"}
	otherRG := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 20, UPFID: "upf-a"}
	ue.Quotas[closedKey] = charging.QuotaState{
		Known: true, Closed: true, Exhausted: true,
		RatingType: charging_datatype.REQ_SUBTYPE_DEBIT,
	}
	ue.Quotas[otherRG] = charging.QuotaState{
		Known: true, Exhausted: true,
		RatingType: charging_datatype.REQ_SUBTYPE_DEBIT,
	}
	active, err := p.rechargeQuotas(ue, 10)
	require.NoError(t, err)
	require.True(t, active)
	require.False(t, ue.Quotas[key].Exhausted)
	require.True(t, ue.Quotas[closedKey].Closed)
	require.True(t, ue.Quotas[closedKey].Exhausted)
	require.True(t, ue.Quotas[otherRG].Exhausted)
	request.InvocationSequenceNumber++
	response, problem = p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 100, false)}
	_, problem = p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.True(t, ue.Quotas[key].Closed)
	active, err = p.rechargeQuotas(ue, 10)
	require.NoError(t, err)
	require.False(t, active, "recharge must not reopen completed quotas")
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 0, true)}
	response, problem = p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	require.False(t, ue.Quotas[key].Closed)
	require.Equal(t, charging_datatype.REQ_SUBTYPE_RESERVE, ue.Quotas[key].RatingType)
}

func TestChargingLegacyUPFIDMustRemainUnambiguous(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			p.serviceUsage, p.accountDebit = fixedQuotaRating(1), grantQuotaRequest
			calls := countAccountCalls(p)
			request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("", 0, true)}
			if existing {
				response, problem := p.ChargingDataUpdate(request, ref)
				require.Nil(t, problem)
				require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
				request.InvocationSequenceNumber++
				request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 0, true)}
			} else {
				request.MultipleUnitUsage = append(request.MultipleUnitUsage, onlineQuotaUsage("upf-a", 0, true))
			}
			before := calls.Load()
			_, problem := p.ChargingDataUpdate(request, ref)
			require.NotNil(t, problem)
			require.Equal(t, int32(400), problem.Status)
			require.Equal(t, before, calls.Load(), "reject ambiguous ownership before any account operation")
		})
	}
}

func TestChargingSettlementPersistenceFailurePreservesQuota(t *testing.T) {
	const outcomeFailure = "outcome"
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		for _, failure := range []string{"restore", "binding", "plan", outcomeFailure, recoveryQuota} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				p, request, ref := chargingRequestSession(t)
				ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
				require.True(t, ok)
				key := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
				ue.Quotas[key] = charging.QuotaState{
					Known: true, Reserved: 100, RatingType: charging_datatype.REQ_SUBTYPE_RESERVE,
				}
				journal := p.journal.(*testJournal)
				switch failure {
				case "restore":
					journal.loadErr = errors.New("quota storage unavailable")
				case recoveryQuota:
					journal.quotaErr = errors.New("quota commit unavailable")
				case "binding":
					p.bindAccount = func(context.Context, *charging_datatype.AccountDebitRequest) ([]byte, error) {
						return nil, errors.New("account binding unavailable")
					}
				case "plan":
					journal.stepErr = errors.New("plan storage unavailable")
				case outcomeFailure:
					journal.outcomeErr = errors.New("outcome storage unavailable")
				}
				calls := countAccountCalls(p)
				for attempt := 0; attempt < 2; attempt++ {
					reply := chargingAPIResponse(t, p, request, ref, operation)
					require.Equal(t, http.StatusServiceUnavailable, reply.Code)
				}
				expected := int32(0)
				if failure == outcomeFailure || failure == recoveryQuota {
					expected = 1
				}
				require.Equal(t, expected, calls.Load())
				require.Equal(t, int64(100), ue.Quotas[key].Reserved)
				require.Equal(t, charging_datatype.REQ_SUBTYPE_RESERVE, ue.Quotas[key].RatingType)
				record := journal.records[(charging.Identity{Session: ref, Sequence: 7}).ID()]
				require.Equal(t, charging.Uncertain, record.State)
				if failure == outcomeFailure {
					require.NotEmpty(t, record.SettlementPlan.Steps["0"])
					require.Empty(t, record.SettlementPlan.Outcomes)
				}
			})
		}
	}
}

func TestChargingReservationFreezesAllRatingResultsBeforeDebit(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	require.True(t, ok)
	key := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
	ue.Quotas[key] = charging.QuotaState{Known: true, RatingType: charging_datatype.REQ_SUBTYPE_RESERVE}
	calls := 0
	accountCalled := false
	shared := &charging_datatype.ServiceUsageResponse{ServiceRating: &charging_datatype.ServiceRating{
		MonetaryTariff: &charging_datatype.MonetaryTariff{RateElement: &charging_datatype.RateElement{
			UnitCost: &charging_datatype.UnitCost{ValueDigits: 1},
		}},
	}}
	p.serviceUsage = func(_ *chf_context.ChfUe, _ *charging_datatype.ServiceUsageRequest) (
		*charging_datatype.ServiceUsageResponse, error,
	) {
		require.False(t, accountCalled, "no RF calls may follow this entry's account debit")
		calls++
		shared.ServiceRating.AllowedUnits = 80
		if calls == 3 {
			shared.ServiceRating.AllowedUnits = 999
			shared.ServiceRating.MonetaryTariff.RateElement.UnitCost.ValueDigits = 2
		}
		return shared, nil
	}
	p.accountDebit = func(_ *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		accountCalled = true
		require.Equal(t, 3, calls)
		record := p.journal.(*testJournal).records[(charging.Identity{Session: ref, Sequence: 7}).ID()]
		var step charging.SettlementStep
		require.NoError(t, chargingdb.DecodeSnapshot(record.SettlementPlan.Steps["0"], &step))
		require.Equal(t, *account, *step.AccountRequest)
		require.NotEmpty(t, step.AccountBinding)
		require.Equal(t, int64(-40), step.Planned.Reserved)
		require.True(t, step.AddGrantedUnits)
		require.Len(t, step.Ratings, 3)
		require.Equal(t, datatype.Unsigned32(80), step.Ratings[1].Response.ServiceRating.AllowedUnits)
		require.Equal(t, datatype.Integer64(1),
			step.Ratings[0].Response.ServiceRating.MonetaryTariff.RateElement.UnitCost.ValueDigits)
		require.Equal(t, datatype.Unsigned64(140), account.MultipleServicesCreditControl.RequestedServiceUnit.CCTotalOctets)
		return &charging_datatype.AccountDebitResponse{
			ResultCode: diam.Success,
			MultipleServicesCreditControl: &charging_datatype.MultipleServicesCreditControl{
				GrantedServiceUnit: &charging_datatype.GrantedServiceUnit{CCTotalOctets: 140},
			},
		}, nil
	}
	response, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	// RF allowed 80, but 100 reserved account units at the next cost of 2 fund only 50.
	require.Equal(t, int32(50), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	require.Equal(t, int64(100), ue.Quotas[key].Reserved)
	require.Equal(t, uint32(2), ue.Quotas[key].UnitCost)
}

func TestChargingRatingFailureBeforeReservationDoesNotDebit(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	calls := countAccountCalls(p)
	ratings := 0
	p.serviceUsage = func(_ *chf_context.ChfUe, _ *charging_datatype.ServiceUsageRequest) (
		*charging_datatype.ServiceUsageResponse, error,
	) {
		ratings++
		if ratings == 3 {
			return nil, errors.New("last RF call failed")
		}
		return &charging_datatype.ServiceUsageResponse{ServiceRating: &charging_datatype.ServiceRating{
			AllowedUnits: 100,
			MonetaryTariff: &charging_datatype.MonetaryTariff{RateElement: &charging_datatype.RateElement{
				UnitCost: &charging_datatype.UnitCost{ValueDigits: 1},
			}},
		}}, nil
	}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
	require.Zero(t, calls.Load())
	ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	require.True(t, ok)
	require.Empty(t, ue.Quotas)
}

func TestChargingRepeatedQuotaOwnerUsesPreviousActualGrant(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	// Preserve the offline threshold when only the online threshold is removed.
	request.MultipleUnitUsage[0].UsedUnitContainer = append(
		[]models.Chf_ConvCharging_UsedUnitContainer{{
			QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_OFFLINE_CHARGING,
		}}, request.MultipleUnitUsage[0].UsedUnitContainer...)
	second := request.MultipleUnitUsage[0]
	request.MultipleUnitUsage = append(request.MultipleUnitUsage, second)
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
	var sessions []string
	p.accountDebit = func(_ *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		sessions = append(sessions, string(account.SessionId))
		response := &charging_datatype.AccountDebitResponse{ResultCode: diam.Success}
		if len(sessions) == 1 {
			response.MultipleServicesCreditControl = &charging_datatype.MultipleServicesCreditControl{
				GrantedServiceUnit:  &charging_datatype.GrantedServiceUnit{CCTotalOctets: 60},
				FinalUnitIndication: &charging_datatype.FinalUnitIndication{FinalUnitAction: charging_datatype.TERMINATE},
			}
		} else {
			require.Equal(t, charging_datatype.TERMINATION_REQUEST, account.CcRequestType)
			require.Equal(t, datatype.Unsigned64(20), account.MultipleServicesCreditControl.UsedServiceUnit.CCTotalOctets)
		}
		return response, nil
	}
	response, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Len(t, sessions, 2)
	require.NotEqual(t, sessions[0], sessions[1])
	require.Equal(t, models.Chf_ConvCharging_FinalUnitAction_TERMINATE,
		response.MultipleUnitInformation[0].FinalUnitIndication.FinalUnitAction)
	require.Equal(t, int32(30000000), response.MultipleUnitInformation[0].VolumeQuotaThreshold)
	record := p.journal.(*testJournal).records[(charging.Identity{Session: ref, Sequence: 7}).ID()]
	require.Len(t, record.SettlementPlan.Steps, 2)
	require.Len(t, record.SettlementPlan.Outcomes, 2)
	var step charging.SettlementStep
	require.NoError(t, chargingdb.DecodeSnapshot(record.SettlementPlan.Steps["1"], &step))
	require.Equal(t, "upf-a", step.UPFID)
	require.True(t, step.Before.Exhausted)
	require.Equal(t, int64(20), step.Before.Reserved)
	require.Equal(t, charging_datatype.REQ_SUBTYPE_DEBIT, step.Before.RatingType)
}

// Simulate MongoDB committing quota+marker while the acknowledgement is lost.
type lostQuotaAnswerJournal struct {
	charging.Journal
}

func (j *lostQuotaAnswerJournal) ApplyQuota(
	ctx context.Context, record *charging.Record, index int,
) (charging.QuotaState, error) {
	if _, err := j.Journal.ApplyQuota(ctx, record, index); err != nil {
		return charging.QuotaState{}, err
	}
	return charging.QuotaState{}, errors.New("quota acknowledgement lost after commit")
}

func TestMongoChargingRestoresQuotaAndDoesNotRepeatCommittedSettlement(t *testing.T) {
	db := testutil.Mongo(t)
	ctx := context.Background()
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
		onlineQuotaUsage("upf-a", 0, true), onlineQuotaUsage("upf-b", 0, true),
	}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	ue, ok := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	require.True(t, ok)
	a := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
	b := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-b"}
	// Discard the quota cache, retaining the live CDR. Complete UE/CDR recovery
	// belongs to a later stage; this verifies the durable quota boundary itself.
	ue.Quotas = nil
	p.journal = charging.NewJournal(db)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-b", 40, true)}
	response, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Equal(t, int32(60), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	require.Equal(t, int64(100), ue.Quotas[a].Reserved)
	require.Equal(t, int64(60), ue.Quotas[b].Reserved)
	requireQuotaBalance(t, db, request.SubscriberIdentifier, 800)
	p.journal = &lostQuotaAnswerJournal{Journal: charging.NewJournal(db)}
	calls := countAccountCalls(p)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 40, false)}
	_, problem = p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
	require.Equal(t, int64(100), ue.Quotas[a].Reserved, "no live update after a lost durable acknowledgement")
	requireQuotaBalance(t, db, request.SubscriberIdentifier, 860)
	var record charging.Record
	require.NoError(t, db.Collection("chf.chargingRequests").FindOne(ctx, bson.M{
		"_id": (charging.Identity{Session: ref, Sequence: request.InvocationSequenceNumber}).ID(),
	}).Decode(&record))
	require.Equal(t, charging.Uncertain, record.State)
	restarted := charging.NewJournal(db)
	makeRecoveryReady(t, db, ref)
	claimed, claimErr := restarted.ClaimRecovery(ctx)
	require.NoError(t, claimErr)
	require.NotNil(t, claimed)
	record = *claimed
	for range 2 {
		state, err := restarted.ApplyQuota(ctx, &record, 0)
		require.NoError(t, err)
		require.Zero(t, state.Reserved)
		require.True(t, state.Closed)
	}
	states, err := restarted.LoadQuotas(ctx, ref)
	require.NoError(t, err)
	require.Zero(t, states[a].Reserved)
	require.Equal(t, int64(60), states[b].Reserved)
	requireQuotaBalance(t, db, request.SubscriberIdentifier, 860)
	_, problem = p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
	request.InvocationSequenceNumber++
	require.NotNil(t, p.ChargingDataRelease(request, ref))
	require.Equal(t, int32(1), calls.Load(), "unresolved CDR work still fences the resource")
}
