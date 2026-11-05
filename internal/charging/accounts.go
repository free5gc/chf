package charging

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
)

type account struct {
	ID    any    `bson:"_id"`
	Quota string `bson:"quota"`
}
type accountResult struct {
	fingerprint string
	body        []byte
	err         error
	expires     time.Time
	timer       *time.Timer
}
type Accounts struct {
	mu       sync.Mutex
	closed   bool
	policies *mongo.Collection
	results  map[string]*accountResult
}

func NewAccounts(db *mongo.Database) *Accounts {
	return &Accounts{policies: db.Collection("policyData.ues.chargingData"), results: make(map[string]*accountResult)}
}

func accountIdentity(request *charging_datatype.AccountDebitRequest) (string, string, string, error) {
	if request == nil || request.SubscriptionId == nil || request.MultipleServicesCreditControl == nil ||
		request.SessionId == "" || request.OriginHost == "" {
		return "", "", "", fmt.Errorf("account request is missing its identity or units")
	}
	if request.SubscriptionId.SubscriptionIdType != charging_datatype.END_USER_IMSI {
		return "", "", "", fmt.Errorf("unsupported subscriber type")
	}
	subscriber := "imsi-" + string(request.SubscriptionId.SubscriptionIdData)
	key, err := Digest([]any{
		request.OriginHost, request.SessionId, request.CcRequestNumber,
		request.SubscriptionId, request.MultipleServicesCreditControl.RatingGroup,
	})
	if err != nil {
		return "", "", "", err
	}
	fingerprint, err := Digest([]any{
		request.RequestedAction, request.CcRequestType,
		request.MultipleServicesCreditControl,
	})
	return subscriber, key, fingerprint, err
}

func calculateAccountResponse(request *charging_datatype.AccountDebitRequest, quota int64) (
	*charging_datatype.AccountDebitResponse, int64, error,
) {
	mscc := request.MultipleServicesCreditControl
	var credit *charging_datatype.MultipleServicesCreditControl
	validUnits := func(units uint64) bool { return units <= math.MaxInt64 }
	switch request.RequestedAction {
	case charging_datatype.REFUND_ACCOUNT:
		if mscc.RequestedServiceUnit == nil || !validUnits(uint64(mscc.RequestedServiceUnit.CCTotalOctets)) {
			return nil, 0, fmt.Errorf("invalid refund units")
		}
		amount := int64(mscc.RequestedServiceUnit.CCTotalOctets)
		if quota > math.MaxInt64-amount {
			return nil, 0, fmt.Errorf("refund overflows balance")
		}
		quota += amount
	case charging_datatype.DIRECT_DEBITING:
		switch request.CcRequestType {
		case charging_datatype.INITIAL_REQUEST, charging_datatype.UPDATE_REQUEST:
			if mscc.RequestedServiceUnit == nil || !validUnits(uint64(mscc.RequestedServiceUnit.CCTotalOctets)) {
				return nil, 0, fmt.Errorf("invalid reservation units")
			}
			amount := int64(mscc.RequestedServiceUnit.CCTotalOctets)
			available := max(quota, 0)
			credit = &charging_datatype.MultipleServicesCreditControl{
				RatingGroup:        mscc.RatingGroup,
				GrantedServiceUnit: &charging_datatype.GrantedServiceUnit{},
			}
			if amount > available {
				amount = available
				credit.FinalUnitIndication = &charging_datatype.FinalUnitIndication{
					FinalUnitAction: charging_datatype.TERMINATE,
				}
			}
			credit.GrantedServiceUnit.CCTotalOctets = datatype.Unsigned64(amount)
			quota -= amount
		case charging_datatype.TERMINATION_REQUEST:
			if mscc.UsedServiceUnit == nil || !validUnits(uint64(mscc.UsedServiceUnit.CCTotalOctets)) {
				return nil, 0, fmt.Errorf("invalid consumed units")
			}
			amount := int64(mscc.UsedServiceUnit.CCTotalOctets)
			if quota < math.MinInt64+amount {
				return nil, 0, fmt.Errorf("debit overflows balance")
			}
			quota -= amount
		default:
			return nil, 0, fmt.Errorf("unsupported account request type")
		}
	default:
		return nil, 0, fmt.Errorf("unsupported account action")
	}
	return &charging_datatype.AccountDebitResponse{
		SessionId: request.SessionId, ResultCode: diam.Success,
		OriginHost: request.DestinationHost, OriginRealm: request.DestinationRealm,
		CcRequestType: request.CcRequestType, CcRequestNumber: request.CcRequestNumber,
		EventTimestamp: datatype.Time(time.Now().UTC()),
		RemainingBalance: &charging_datatype.RemainingBalance{
			UnitValue: &charging_datatype.UnitValue{ValueDigits: datatype.Integer64(quota), Exponent: 0},
		},
		MultipleServicesCreditControl: credit,
	}, quota, nil
}

// Apply reads the same balance Webconsole edits. The response cache exists only
// in this ABMF process; no replay history or account copy is stored in MongoDB.
func (a *Accounts) Apply(
	ctx context.Context, request *charging_datatype.AccountDebitRequest,
) (*charging_datatype.AccountDebitResponse, error) {
	subscriber, id, fingerprint, err := accountIdentity(request)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrStopped
	}
	now := time.Now()
	// CHF freezes this ID before sending a debit. Its issue time cannot be
	// refreshed by a late retransmission with a different EventTimestamp.
	parts := strings.SplitN(string(request.SessionId), ";", 3)
	if len(parts) != 3 || parts[0] != "chf" || parts[2] == "" {
		return nil, ErrExpired
	}
	issued, issueTimeErr := strconv.ParseInt(parts[1], 10, 64)
	if issueTimeErr != nil {
		return nil, ErrExpired
	}
	stamp := time.Unix(issued, 0)
	if now.Sub(stamp) >= AccountReplayWindow || stamp.After(now.Add(time.Minute)) {
		return nil, ErrExpired
	}
	if previous, ok := a.results[id]; ok {
		if previous.fingerprint != fingerprint {
			return nil, ErrConflict
		}
		if previous.err != nil {
			return nil, previous.err
		}
		var response charging_datatype.AccountDebitResponse
		if err = DecodeSnapshot(previous.body, &response); err != nil {
			return nil, err
		}
		return &response, nil
	}
	if len(a.results) >= MaxAccountResults {
		return nil, ErrCapacity
	}
	filter := bson.M{"ueId": subscriber, "ratingGroup": request.MultipleServicesCreditControl.RatingGroup}
	for attempt := 0; attempt < 32; attempt++ {
		var balance account
		if err = a.policies.FindOne(ctx, filter).Decode(&balance); err != nil {
			return nil, err
		}
		quota, parseErr := strconv.ParseInt(balance.Quota, 10, 64)
		if parseErr != nil {
			return nil, parseErr
		}
		response, remaining, calculateErr := calculateAccountResponse(request, quota)
		if calculateErr != nil {
			return nil, calculateErr
		}
		body, encodeErr := EncodeSnapshot(response)
		if encodeErr != nil {
			return nil, encodeErr
		}
		// Do not overwrite a concurrent Webconsole adjustment with a stale balance.
		result, updateErr := a.policies.UpdateOne(ctx, bson.M{"_id": balance.ID, "quota": balance.Quota},
			bson.M{"$set": bson.M{"quota": strconv.FormatInt(remaining, 10)}})
		if updateErr != nil {
			// A lost database reply does not prove the debit failed. Keep this operation
			// blocked in memory instead of issuing a second debit with the same identity.
			a.cacheResult(id, &accountResult{
				fingerprint: fingerprint, expires: stamp.Add(AccountReplayWindow),
				err: fmt.Errorf("account write outcome unknown: %w", updateErr),
			})
			return nil, a.results[id].err
		}
		if result.MatchedCount == 1 {
			a.cacheResult(id, &accountResult{
				fingerprint: fingerprint, body: body, expires: stamp.Add(AccountReplayWindow),
			})
			return response, nil
		}
	}
	return nil, fmt.Errorf("account balance changed too often; retry the same operation")
}

// cacheResult is called with mu held. Replays keep the original issue-time deadline.
func (a *Accounts) cacheResult(id string, result *accountResult) {
	if previous := a.results[id]; previous != nil && previous.timer != nil {
		previous.timer.Stop()
	}
	a.results[id] = result
	result.timer = time.AfterFunc(time.Until(result.expires), func() { a.expire(id, result) })
}

func (a *Accounts) expire(id string, expected *accountResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// A callback already dispatched before cancellation must not remove a replacement.
	if !a.closed && a.results[id] == expected {
		delete(a.results, id)
	}
}

// Stop cancels cached-result timers and prevents further account operations.
func (a *Accounts) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	for _, result := range a.results {
		if result.timer != nil {
			result.timer.Stop()
		}
	}
	a.results = nil
}
