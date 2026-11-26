// Package charging tracks charging requests for the lifetime of this process.
package charging

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrConflict = errors.New("charging invocation reused with different content")
	ErrPending  = errors.New("charging session has an unresolved operation")
	ErrExpired  = errors.New("charging invocation is outside the retry window")
	ErrCapacity = errors.New("charging replay cache is full")
	ErrStopped  = errors.New("charging request cache is stopped")
)

// ReplayWindow exceeds SMF's bounded retry budget. Account results live longer
// than both request replay and uncertain settlement; expired debits are rejected.
const (
	ReplayWindow = 5 * time.Minute
	// Covers the current SMF nominal retry budget: 4*5s + 2s + 4s + 8s = 34s.
	UncertainTimeout    = time.Minute
	AccountReplayWindow = 10 * time.Minute
	MaxCachedRequests   = 4096
	MaxReplayBytes      = 32 << 20
	MaxAccountResults   = 16384
)

const (
	Retired    = "retired"
	Processing = "processing"
	Succeeded  = "succeeded"
	Rejected   = "rejected"
	Uncertain  = "uncertain"
)

type Identity struct {
	Request     []byte
	Session     string
	Sequence    int32
	Operation   string
	Fingerprint string
}

func Digest(v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (i Identity) ID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", i.Session, i.Sequence)))
	return hex.EncodeToString(sum[:])
}

type Result struct {
	Status int
	Body   []byte
}
type Record struct {
	ID string
	Identity
	Failure        string
	Retry          bool
	State          string
	Result         Result
	CreatedAt      time.Time
	SettlementPlan *SettlementPlan
}
type cachedRequest struct {
	record    Record
	running   bool
	expires   time.Time
	completed *list.Element
	deadline  time.Time
	timer     *time.Timer
	onExpired func(Record)
}

// RequestCache is deliberately empty after restart. It has no database or file backing.
// Failed requests retain their original steps; only the same invocation can retry them.
type RequestCache struct {
	// RetryRetention starts on the first uncertain result. Zero uses UncertainTimeout.
	// Configure this before serving requests to cover the consumer's full retry budget.
	RetryRetention time.Duration
	mu             sync.Mutex
	closed         bool
	cleanups       sync.WaitGroup
	records        map[string]*cachedRequest
	active         map[string]string
	completed      list.List
	replayBytes    int
}

func (c *RequestCache) Begin(identity Identity) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrStopped
	}
	if c.records == nil {
		c.records = make(map[string]*cachedRequest)
		c.active = make(map[string]string)
	}
	now := time.Now()
	id := identity.ID()
	if entry := c.records[id]; entry != nil && !entry.running &&
		(entry.record.State == Succeeded || entry.record.State == Rejected) && !now.Before(entry.expires) {
		c.remove(id)
	}
	if entry := c.records[id]; entry != nil {
		record := entry.record
		if record.State == Retired || (!entry.deadline.IsZero() && !now.Before(entry.deadline)) {
			return nil, ErrExpired
		}
		if record.Fingerprint != identity.Fingerprint || record.Operation != identity.Operation {
			return nil, ErrConflict
		}
		if entry.running {
			return nil, ErrPending
		}
		if record.State == Uncertain {
			entry.running = true
			record.State = Processing
			record.Retry = true
		}
		return &record, nil
	}
	if c.active[identity.Session] != "" {
		return nil, ErrPending
	}
	if len(c.records) >= MaxCachedRequests {
		// Resource watermarks reject old invocations even after response eviction.
		oldest := c.completed.Front()
		if oldest == nil {
			return nil, ErrCapacity
		}
		c.remove(oldest.Value.(string))
	}

	record := Record{
		ID: id, Identity: identity, State: Processing, CreatedAt: now.UTC(),
		SettlementPlan: &SettlementPlan{Steps: make(map[string][]byte), Outcomes: make(map[string][]byte)},
	}
	c.records[id] = &cachedRequest{record: record, running: true}
	c.active[identity.Session] = id
	return &record, nil
}

func (c *RequestCache) Finish(record *Record, result Result, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	record.State, record.Result = state, result
	record.Request, record.SettlementPlan, record.Failure = nil, nil, ""
	now := time.Now()
	c.remove(record.ID)
	delete(c.active, record.Session)
	if len(result.Body) > MaxReplayBytes {
		return
	}
	for c.replayBytes+len(result.Body) > MaxReplayBytes {
		c.remove(c.completed.Front().Value.(string))
	}
	entry := &cachedRequest{
		record: *record, expires: now.Add(ReplayWindow), completed: c.completed.PushBack(record.ID),
	}
	id := record.ID
	c.records[id] = entry
	c.replayBytes += len(result.Body)
	// Successful and rejected responses expire without another request or sweep.
	entry.timer = time.AfterFunc(ReplayWindow, func() { c.expire(id, entry) })
}

// Fail starts a one-shot timer on the first uncertain result. A retry keeps
// that deadline; after expiry, another failure is retired asynchronously because
// the caller can still hold the UE lock needed by onExpired.
func (c *RequestCache) Fail(record *Record, onExpired func(Record)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	entry := c.records[record.ID]
	if entry == nil || entry.record.State == Retired {
		return
	}
	record.State = Uncertain
	entry.record, entry.running = *record, false
	now := time.Now()
	if entry.deadline.IsZero() {
		retention := c.RetryRetention
		if retention == 0 {
			retention = UncertainTimeout
		}
		entry.deadline = now.Add(retention)
		entry.onExpired = onExpired
	}
	if entry.timer == nil {
		id := record.ID
		entry.timer = time.AfterFunc(max(0, entry.deadline.Sub(now)), func() { c.expire(id, entry) })
	}
}

func (c *RequestCache) expire(id string, expected *cachedRequest) {
	c.mu.Lock()
	if c.closed || c.records[id] != expected || expected.record.State == Retired {
		c.mu.Unlock()
		return
	}
	expected.timer = nil
	if expected.completed != nil {
		// Reclaim only this response. A live resource keeps its sequence watermark,
		// quota and CDR state, so an expired response cannot cause another debit.
		c.remove(id)
		c.mu.Unlock()
		return
	}
	if expected.running {
		// Let the retry finish. Success cancels expiry; Fail will schedule immediate
		// retirement against the same deadline if the result is still uncertain.
		c.mu.Unlock()
		return
	}
	expected.record.State = Retired
	record, onExpired := expected.record, expected.onExpired
	c.cleanups.Add(1)
	c.mu.Unlock()
	defer c.cleanups.Done()
	onExpired(record)
}

// Stop cancels scheduled timers and waits for already-claimed resource cleanup.
// No new callbacks can be claimed once closed is set under the same mutex.
func (c *RequestCache) Stop() {
	c.mu.Lock()
	c.closed = true
	for id := range c.records {
		c.remove(id)
	}
	c.active = nil
	c.mu.Unlock()
	c.cleanups.Wait()
}

func (c *RequestCache) Retire(session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, entry := range c.records {
		if entry.record.Session == session {
			c.remove(id)
		}
	}
	delete(c.active, session)
}

func (c *RequestCache) remove(id string) {
	if entry := c.records[id]; entry != nil {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		if entry.completed != nil {
			c.completed.Remove(entry.completed)
			c.replayBytes -= len(entry.record.Result.Body)
		}
	}
	delete(c.records, id)
}
