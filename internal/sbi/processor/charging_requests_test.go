package processor

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	chf_context "github.com/free5gc/chf/internal/context"
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
