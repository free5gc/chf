package charging

import (
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequestCacheCompactsBoundsAndExpiresCompletedResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache RequestCache
		defer cache.Stop()
		for sequence := range MaxCachedRequests + 50 {
			record, err := cache.Begin(Identity{
				Session: "resource", Sequence: int32(sequence), Operation: "update",
				Fingerprint: "digest", Request: []byte("large body"),
			})
			require.NoError(t, err)
			record.SettlementPlan.Steps["0"] = []byte("large step")
			cache.Finish(record, Result{Status: 200, Body: []byte("response")}, Succeeded)
		}
		cache.mu.Lock()
		count, active := len(cache.records), len(cache.active)
		var records []Record
		for _, entry := range cache.records {
			records = append(records, entry.record)
		}
		cache.mu.Unlock()
		require.Equal(t, MaxCachedRequests, count)
		require.Zero(t, active)
		for _, record := range records {
			require.Nil(t, record.Request)
			require.Nil(t, record.SettlementPlan)
		}
		identity := Identity{
			Session: "resource", Sequence: MaxCachedRequests + 49, Operation: "update", Fingerprint: "digest",
		}
		replay, err := cache.Begin(identity)
		require.NoError(t, err)
		require.Equal(t, Succeeded, replay.State)
		require.Equal(t, []byte("response"), replay.Result.Body)
		time.Sleep(ReplayWindow)
		synctest.Wait()
		require.Empty(t, cache.records)
		require.Zero(t, cache.completed.Len())
		require.Zero(t, cache.replayBytes)
	})
}

func TestRequestCacheIdleResponseExpiryDoesNotExtendOnReplay(t *testing.T) {
	for _, state := range []string{Succeeded, Rejected} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache RequestCache
				defer cache.Stop()
				identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
				record, err := cache.Begin(identity)
				require.NoError(t, err)
				time.Sleep(time.Hour) // Replay retention starts on completion, not Begin.
				cache.Finish(record, Result{Status: 200, Body: []byte("response")}, state)
				time.Sleep(ReplayWindow / 2)
				replay, beginErr := cache.Begin(identity)
				require.NoError(t, beginErr)
				require.Equal(t, state, replay.State)
				time.Sleep(ReplayWindow / 2)
				synctest.Wait()
				require.Empty(t, cache.records, "idle completed results must expire without polling")
				require.Zero(t, cache.replayBytes)
			})
		})
	}
}

func TestRequestCacheStartsRetentionOnFirstFailureAndKeepsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := RequestCache{RetryRetention: 90 * time.Second}
		defer cache.Stop()
		expired := make(chan Record, 1)
		onExpired := func(record Record) { expired <- record }
		identity := Identity{Session: "resource", Sequence: 1, Operation: "update", Fingerprint: "digest"}
		record, err := cache.Begin(identity)
		require.NoError(t, err)
		time.Sleep(2 * time.Minute) // Initial processing time does not consume retention.
		require.Equal(t, Processing, cache.records[record.ID].record.State)
		cache.Fail(record, onExpired)
		firstDeadline := cache.records[record.ID].deadline
		time.Sleep(45 * time.Second)
		record, err = cache.Begin(identity)
		require.NoError(t, err)
		require.True(t, record.Retry)
		cache.Fail(record, onExpired)
		require.Equal(t, firstDeadline, cache.records[record.ID].deadline)
		time.Sleep(44 * time.Second)
		select {
		case <-expired:
			t.Fatal("retired before the retention deadline")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case retired := <-expired:
			require.Equal(t, Retired, retired.State)
		default:
			t.Fatal("expiry must run without any new API request or sweep")
		}
		_, err = cache.Begin(identity)
		require.ErrorIs(t, err, ErrExpired)
		identity.Sequence++
		_, err = cache.Begin(identity)
		require.ErrorIs(t, err, ErrPending, "keep the fence until resource removal")
		cache.Retire(identity.Session)
		require.Empty(t, cache.records)
		require.Empty(t, cache.active)
	})
}

func TestRequestCacheExpiryWaitsForRunningRetry(t *testing.T) {
	for _, result := range []string{"success", "failure"} {
		t.Run(result, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache RequestCache
				defer cache.Stop()
				expired := make(chan Record, 1)
				onExpired := func(record Record) { expired <- record }
				identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
				record, err := cache.Begin(identity)
				require.NoError(t, err)
				cache.Fail(record, onExpired)
				record, err = cache.Begin(identity)
				require.NoError(t, err)
				time.Sleep(UncertainTimeout + time.Second)
				synctest.Wait()
				select {
				case <-expired:
					t.Fatal("an executing retry must not be retired")
				default:
				}
				if result == "success" {
					cache.Finish(record, Result{Status: 200}, Succeeded)
					time.Sleep(UncertainTimeout)
					synctest.Wait()
					select {
					case <-expired:
						t.Fatal("success must cancel expiry")
					default:
					}
					replay, beginErr := cache.Begin(identity)
					require.NoError(t, beginErr)
					require.Equal(t, Succeeded, replay.State)
				} else {
					cache.Fail(record, onExpired)
					synctest.Wait()
					select {
					case retired := <-expired:
						require.Equal(t, Retired, retired.State)
					default:
						t.Fatal("failure after the deadline must retire immediately")
					}
				}
			})
		})
	}
}

func TestRequestCacheCancelsPendingTimers(t *testing.T) {
	for _, action := range []string{"success", "retire", "stop"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache RequestCache
				defer cache.Stop()
				expired := make(chan Record, 1)
				identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
				record, err := cache.Begin(identity)
				require.NoError(t, err)
				cache.Fail(record, func(record Record) { expired <- record })
				switch action {
				case "success":
					record, err = cache.Begin(identity)
					require.NoError(t, err)
					cache.Finish(record, Result{Status: 200}, Succeeded)
				case "retire":
					cache.Retire(identity.Session)
				case "stop":
					cache.Stop()
				}
				time.Sleep(2 * UncertainTimeout)
				synctest.Wait()
				select {
				case <-expired:
					t.Fatal("a canceled callback ran")
				default:
				}
				if action == "stop" {
					_, err = cache.Begin(identity)
					require.ErrorIs(t, err, ErrStopped)
				}
			})
		})
	}
}

func TestRequestCacheDoesNotEvictInFlightWorkForCapacity(t *testing.T) {
	var cache RequestCache
	for sequence := range MaxCachedRequests {
		_, err := cache.Begin(Identity{Session: fmt.Sprint(sequence), Sequence: 1})
		require.NoError(t, err)
	}
	_, err := cache.Begin(Identity{Session: "overflow", Sequence: 1})
	require.ErrorIs(t, err, ErrCapacity)
	require.Len(t, cache.active, MaxCachedRequests)
}

func TestRequestCacheBoundsResponseBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache RequestCache
		defer cache.Stop()
		body := make([]byte, MaxReplayBytes/2)
		for sequence := range 3 {
			record, err := cache.Begin(Identity{Session: "resource", Sequence: int32(sequence)})
			require.NoError(t, err)
			cache.Finish(record, Result{Status: 200, Body: body}, Succeeded)
		}
		cache.mu.Lock()
		count, size := len(cache.records), cache.replayBytes
		cache.mu.Unlock()
		require.Equal(t, 2, count)
		require.Equal(t, MaxReplayBytes, size)
		time.Sleep(ReplayWindow)
		synctest.Wait()
		require.Empty(t, cache.records)
		require.Zero(t, cache.replayBytes)
	})
}

func TestRequestCacheIgnoresStaleTimerCallbacks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache RequestCache
		defer cache.Stop()
		expired := make(chan Record, 2)
		onExpired := func(record Record) { expired <- record }
		identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
		record, err := cache.Begin(identity)
		require.NoError(t, err)
		cache.Fail(record, onExpired)
		old := cache.records[record.ID]
		record, err = cache.Begin(identity)
		require.NoError(t, err)
		cache.Finish(record, Result{Status: 200}, Succeeded)
		// Stop cannot recall an already-dispatched callback. It must verify the
		// original entry rather than retiring a completed or replacement invocation.
		cache.expire(record.ID, old)
		require.Equal(t, Succeeded, cache.records[record.ID].record.State)
		cache.Retire(identity.Session)
		record, err = cache.Begin(identity)
		require.NoError(t, err)
		cache.Fail(record, onExpired)
		cache.expire(record.ID, old)
		cache.mu.Lock()
		state := cache.records[record.ID].record.State
		cache.mu.Unlock()
		require.Equal(t, Uncertain, state)
		time.Sleep(UncertainTimeout)
		synctest.Wait()
		require.Len(t, expired, 1, "only the replacement invocation's timer may expire")
	})
}

func TestRequestCacheShutdownWaitsForClaimedCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache RequestCache
		started, proceed, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
		allow := sync.OnceFunc(func() { close(proceed) })
		defer allow()
		identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
		record, err := cache.Begin(identity)
		require.NoError(t, err)
		cache.Fail(record, func(record Record) {
			close(started)
			<-proceed
			cache.Retire(record.Session)
		})
		time.Sleep(UncertainTimeout)
		<-started
		go func() { cache.Stop(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("shutdown returned before resource cleanup completed")
		default:
		}
		allow()
		<-stopped
		require.Empty(t, cache.records)
		require.Empty(t, cache.active)
	})
}

func TestRequestCacheIgnoresStaleResponseTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache RequestCache
		defer cache.Stop()
		identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
		record, err := cache.Begin(identity)
		require.NoError(t, err)
		cache.Finish(record, Result{Status: 200, Body: []byte("old response")}, Succeeded)
		old := cache.records[record.ID]
		cache.Retire(identity.Session)
		record, err = cache.Begin(identity)
		require.NoError(t, err)
		cache.Finish(record, Result{Status: 200, Body: []byte("new response")}, Succeeded)
		// Simulate an old callback already dispatched when the old timer was stopped.
		cache.expire(record.ID, old)
		replay, err := cache.Begin(identity)
		require.NoError(t, err)
		require.Equal(t, []byte("new response"), replay.Result.Body)
		time.Sleep(ReplayWindow)
		synctest.Wait()
		require.Empty(t, cache.records)
		require.Zero(t, cache.replayBytes)
	})
}

func TestResponseExpiryPreservesAnotherInvocationFence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache RequestCache
		defer cache.Stop()
		identity := Identity{Session: "resource", Sequence: 1, Operation: "update"}
		record, err := cache.Begin(identity)
		require.NoError(t, err)
		cache.Finish(record, Result{Status: 200}, Succeeded)
		time.Sleep(ReplayWindow - UncertainTimeout/2)
		identity.Sequence++
		pending, err := cache.Begin(identity)
		require.NoError(t, err)
		expired := make(chan Record, 1)
		cache.Fail(pending, func(record Record) { expired <- record })
		time.Sleep(UncertainTimeout / 2)
		synctest.Wait() // The first response is gone; the second invocation still owns its fence.
		next := identity
		next.Sequence++
		_, err = cache.Begin(next)
		require.ErrorIs(t, err, ErrPending)
		time.Sleep(UncertainTimeout / 2)
		synctest.Wait()
		require.Len(t, expired, 1)
		require.Equal(t, pending.ID, (<-expired).ID)
	})
}
