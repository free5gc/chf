// Package charging tracks charging requests for the lifetime of this process.
package charging

import (
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
)

const (
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
	State          string
	Result         Result
	CreatedAt      time.Time
	SettlementPlan *SettlementPlan
}
type cachedRequest struct {
	record  Record
	running bool
}

// RequestCache is deliberately empty after restart. It has no database or file backing.
// Failed requests retain their original steps; only the same invocation can retry them.
type RequestCache struct {
	mu      sync.Mutex
	records map[string]*cachedRequest
	active  map[string]string
}

func (c *RequestCache) Begin(identity Identity) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.records == nil {
		c.records = make(map[string]*cachedRequest)
		c.active = make(map[string]string)
	}
	id := identity.ID()
	if entry := c.records[id]; entry != nil {
		record := entry.record
		if record.Fingerprint != identity.Fingerprint || record.Operation != identity.Operation {
			return nil, ErrConflict
		}
		if entry.running {
			return nil, ErrPending
		}
		if record.State == Uncertain {
			entry.running = true
			record.State = Processing
		}
		return &record, nil
	}
	if c.active[identity.Session] != "" {
		return nil, ErrPending
	}
	record := Record{
		ID: id, Identity: identity, State: Processing, CreatedAt: time.Now().UTC(),
		SettlementPlan: &SettlementPlan{Steps: make(map[string][]byte), Outcomes: make(map[string][]byte)},
	}
	c.records[id] = &cachedRequest{record: record, running: true}
	c.active[identity.Session] = id
	return &record, nil
}

func (c *RequestCache) Finish(record *Record, result Result, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record.State, record.Result = state, result
	c.records[record.ID] = &cachedRequest{record: *record}
	delete(c.active, record.Session)
}

func (c *RequestCache) Fail(record *Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record.State = Uncertain
	c.records[record.ID] = &cachedRequest{record: *record}
}
