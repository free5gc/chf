package processor

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v2"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/testutil"
	"github.com/free5gc/chf/pkg/app"
	"github.com/free5gc/chf/pkg/factory"
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
			var written []*cdrType.CHFRecord
			p.cdrWriter = func(_ string, records []*cdrType.CHFRecord) error {
				written = records
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
			volume, entries := cdrUsageTotals(written)
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
	t.Cleanup(fresh.Stop)
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

func TestChargingReleaseCleansResourceAndPreservesOtherSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, request, ref := chargingRequestSession(t)
		ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
		calls := countAccountCalls(p)
		otherRef := ref + "-other"
		otherCDR, err := p.OpenCDR(request, ue, otherRef, false)
		require.NoError(t, err)
		ue.Cdr[otherRef] = otherCDR
		ue.Records = append(ue.Records, otherCDR)
		key := charging.QuotaKey{ChargingDataRef: ref, RatingGroup: 10, UPFID: "upf-a"}
		ue.PendingRecharges = map[charging.QuotaKey]bool{key: true}
		first := chargingAPIResponse(t, p, request, ref, chargingReleaseOperation)
		require.Equal(t, http.StatusNoContent, first.Code, first.Body.String())
		require.NotContains(t, ue.Cdr, ref)
		require.NotContains(t, ue.CDRProgress, ref)
		require.NotContains(t, ue.Quotas, key)
		require.NotContains(t, ue.PendingRecharges, key)
		require.Len(t, ue.Records, 1)
		require.Equal(t, otherCDR, ue.Records[0])
		_, exists := chf_context.GetSelf().ChfUeFindBySupi(ue.Supi)
		require.True(t, exists, "the other session still owns the UE")
		require.Nil(t, p.ChargingDataRelease(request, ref), "release response can still be replayed")
		require.Equal(t, int32(1), calls.Load())
		require.Nil(t, p.ChargingDataRelease(request, otherRef))
		_, exists = chf_context.GetSelf().ChfUeFindBySupi(ue.Supi)
		require.False(t, exists)
		require.Empty(t, ue.Cdr)
		require.Empty(t, ue.CDRProgress)
		require.Empty(t, ue.Records)
		// The short response cache expires. An old request must now return 404,
		// even if a new session for the same SUPI has already been created.
		time.Sleep(charging.ReplayWindow)
		synctest.Wait()
		chf_context.GetSelf().UePool.Store(ue.Supi, &chf_context.ChfUe{Supi: ue.Supi})
		problem := p.ChargingDataRelease(request, ref)
		require.NotNil(t, problem)
		require.Equal(t, int32(http.StatusNotFound), problem.Status)
		require.Equal(t, int32(2), calls.Load())
	})
}

func TestChargingExpiredResponseCannotDebitOldSequenceAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, request, ref := chargingRequestSession(t)
		calls := countAccountCalls(p)
		_, problem := p.ChargingDataUpdate(request, ref)
		require.Nil(t, problem)
		time.Sleep(charging.ReplayWindow)
		synctest.Wait()
		ue, exists := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
		require.True(t, exists, "response expiry must not retire a live session")
		require.Contains(t, ue.Cdr, ref)
		require.Equal(t, request.InvocationSequenceNumber, ue.CDRProgress[ref].Sequence)
		_, problem = p.ChargingDataUpdate(request, ref)
		require.NotNil(t, problem)
		require.Equal(t, int32(http.StatusConflict), problem.Status)
		require.Equal(t, int32(1), calls.Load())
		request.InvocationSequenceNumber++
		_, problem = p.ChargingDataUpdate(request, ref)
		require.Nil(t, problem)
		require.Equal(t, int32(2), calls.Load(), "a new invocation can still charge")
	})
}

func TestChargingAbandonedUpdateRetiresResourceAndRejectsLateRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, request, ref := chargingRequestSession(t)
		calls := countAccountCalls(p)
		original := p.accountDebit
		p.accountDebit = func(ue *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
			*charging_datatype.AccountDebitResponse, error,
		) {
			_, err := original(ue, account)
			require.NoError(t, err)
			return nil, errors.New("account reply lost after debit")
		}
		_, problem := p.ChargingDataUpdate(request, ref)
		require.NotNil(t, problem)
		ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
		require.True(t, ue.CULock.TryLock(), "uncertain is a logical fence, not a held mutex")
		ue.CULock.Unlock()
		next := request
		next.InvocationSequenceNumber++
		require.NotNil(t, p.ChargingDataRelease(next, ref))
		time.Sleep(charging.UncertainTimeout)
		synctest.Wait()
		require.Empty(t, ue.Cdr)
		require.Empty(t, ue.CDRProgress)
		require.Empty(t, ue.Quotas)
		require.Empty(t, ue.Records)
		_, exists := chf_context.GetSelf().ChfUeFindBySupi(ue.Supi)
		require.False(t, exists)
		_, problem = p.ChargingDataUpdate(request, ref)
		require.NotNil(t, problem)
		require.Equal(t, int32(http.StatusNotFound), problem.Status)
		problem = p.ChargingDataRelease(next, ref)
		require.NotNil(t, problem)
		require.Equal(t, int32(http.StatusNotFound), problem.Status, "no unresolved-operation fence remains")
		require.Equal(t, int32(1), calls.Load(), "retirement never retries or refunds an ambiguous debit")
	})
}

func TestChargingRetryRunningAtExpiryFinishesBeforeCleanup(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, request, ref := chargingRequestSession(t)
				started, proceed := make(chan struct{}), make(chan struct{})
				allow := sync.OnceFunc(func() { close(proceed) })
				defer allow()
				original := p.accountDebit
				attempts := 0
				p.accountDebit = func(ue *chf_context.ChfUe, account *charging_datatype.AccountDebitRequest) (
					*charging_datatype.AccountDebitResponse, error,
				) {
					attempts++
					if attempts == 1 {
						return nil, errors.New("account reply lost")
					}
					close(started)
					<-proceed
					if outcome == "failure" {
						return nil, errors.New("account reply still unknown")
					}
					return original(ue, account)
				}
				_, problem := p.ChargingDataUpdate(request, ref)
				require.NotNil(t, problem)
				done := make(chan *models.ProblemDetails, 1)
				go func() {
					_, retryProblem := p.ChargingDataUpdate(request, ref)
					done <- retryProblem
				}()
				<-started
				time.Sleep(charging.UncertainTimeout + time.Second)
				synctest.Wait()
				_, exists := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
				require.True(t, exists, "the timer must not remove a running retry's resource")
				allow()
				problem = <-done
				synctest.Wait()
				ue, exists := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
				if outcome == "success" {
					require.Nil(t, problem)
					require.True(t, exists)
					require.Contains(t, ue.Cdr, ref)
					// Let the old deadline pass again: a stopped callback cannot retire success.
					time.Sleep(charging.UncertainTimeout)
					synctest.Wait()
					_, exists = chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
					require.True(t, exists)
				} else {
					require.NotNil(t, problem)
					require.False(t, exists, "a failed retry past its original deadline must be cleaned up")
					_, problem = p.ChargingDataUpdate(request, ref)
					require.NotNil(t, problem)
					require.Equal(t, int32(http.StatusNotFound), problem.Status)
				}
			})
		})
	}
}

func TestChargingRetryRetentionConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        time.Duration
		invalid     bool
	}{
		{"default", "", charging.UncertainTimeout, false},
		{"configured", "90s", 90 * time.Second, false},
		{"negative", "-1s", 0, true},
		{"account replay boundary", "10m", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &factory.Config{Configuration: &factory.Configuration{}}
			if tc.value != "" {
				require.NoError(t, yaml.Unmarshal([]byte("configuration:\n  chargingRetryRetention: "+tc.value), cfg))
			}
			mockApp := app.NewMockApp(gomock.NewController(t))
			mockApp.EXPECT().Config().Return(cfg)
			p, err := NewProcessor(mockApp)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, p.requests.RetryRetention)
		})
	}
}
