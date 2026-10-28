package processor

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/testutil"
	"github.com/free5gc/openapi/models"
)

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
			// The same running processor retains the response after UE removal.
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

func TestChargingRetryUsesOriginalSettlementAndWritesUsageOnce(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			p, request, ref := chargingRequestSession(t)
			calls := countAccountCalls(p)
			writes := 0
			p.cdrWriter = func(_ string, _ []*cdrType.CHFRecord) error {
				writes++
				if writes == 1 {
					return errors.New("CDR output unavailable")
				}
				return nil
			}
			first := chargingAPIResponse(t, p, request, ref, operation)
			require.Equal(t, http.StatusServiceUnavailable, first.Code)
			require.Equal(t, int32(1), calls.Load())
			// A new sequence cannot bypass the pending operation.
			next := request
			next.InvocationSequenceNumber++
			require.Equal(t, http.StatusServiceUnavailable, chargingAPIResponse(t, p, next, ref, operation).Code)
			// A retry uses the memory snapshot even if the rating service is unavailable.
			p.serviceUsage = func(
				*chf_context.ChfUe, *charging_datatype.ServiceUsageRequest,
			) (*charging_datatype.ServiceUsageResponse, error) {
				t.Fatal("retry must not repeat rating")
				return nil, nil
			}
			request.RetransmissionIndicator = true
			retried := chargingAPIResponse(t, p, request, ref, operation)
			status := http.StatusOK
			if operation == chargingReleaseOperation {
				status = http.StatusNoContent
			}
			require.Equal(t, status, retried.Code, retried.Body.String())
			require.Equal(t, int32(1), calls.Load())
			ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
			volume, entries := cdrUsageTotals(ue.Records)
			require.Equal(t, int64(40), volume)
			require.Equal(t, 1, entries)
		})
	}
}

func TestChargingNewProcessorDoesNotRestoreRemovedResource(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	require.Equal(t, http.StatusOK, chargingAPIResponse(t, p, request, ref, chargingUpdateOperation).Code)
	chf_context.GetSelf().UePool.Delete(request.SubscriberIdentifier)
	fresh := &Processor{}
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		request.InvocationSequenceNumber++
		require.Equal(t, http.StatusNotFound, chargingAPIResponse(t, fresh, request, ref, operation).Code)
	}
}

func TestMongoChargingRetryAfterLostAccountReplyDoesNotDebitTwice(t *testing.T) {
	for _, operation := range []string{chargingUpdateOperation, chargingReleaseOperation} {
		t.Run(operation, func(t *testing.T) {
			db := testutil.Mongo(t)
			p, request, ref := chargingRequestSession(t)
			p.serviceUsage = fixedQuotaRating(1)
			useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "100")
			original := p.accountDebit
			calls := 0
			p.accountDebit = func(ue *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
				*charging_datatype.AccountDebitResponse, error,
			) {
				response, err := original(ue, account)
				if err != nil {
					return nil, err
				}
				calls++
				if calls == 1 {
					return nil, errors.New("account reply lost after debit")
				}
				return response, nil
			}
			require.Equal(t, http.StatusServiceUnavailable, chargingAPIResponse(t, p, request, ref, operation).Code)
			requireQuotaBalance(t, db, request.SubscriberIdentifier, 60)
			request.RetransmissionIndicator = true
			status := http.StatusOK
			if operation == chargingReleaseOperation {
				status = http.StatusNoContent
			}
			reply := chargingAPIResponse(t, p, request, ref, operation)
			require.Equal(t, status, reply.Code, reply.Body.String())
			require.Equal(t, 2, calls)
			requireQuotaBalance(t, db, request.SubscriberIdentifier, 60)
		})
	}
}

func TestChargingRechargeWaitsForPendingInvocation(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{onlineQuotaUsage("upf-a", 0, true)}
	grants := 0
	p.accountDebit = func(ue *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		response, err := grantQuotaRequest(ue, account)
		grants++
		if grants == 1 {
			response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets = 50
			response.MultipleServicesCreditControl.FinalUnitIndication = &charging_datatype.FinalUnitIndication{
				FinalUnitAction: charging_datatype.TERMINATE,
			}
		}
		return response, err
	}
	p.cdrWriter = func(string, []*cdrType.CHFRecord) error { return errors.New("CDR output unavailable") }
	_, problem := p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	active, err := p.rechargeQuotas(ue, 10)
	require.NoError(t, err)
	require.True(t, active)
	p.cdrWriter = nil
	response, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Equal(t, int32(50), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	require.Equal(t, 1, grants)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume = 50
	response, problem = p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Equal(t, int32(100), response.MultipleUnitInformation[0].GrantedUnit.TotalVolume)
	require.Equal(t, 2, grants)
}
