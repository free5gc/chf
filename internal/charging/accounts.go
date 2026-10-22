package charging

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	chargingdb "github.com/free5gc/chf/internal/db"
)

// Keep the operation and balance in the same Mongo document: this also works
// with standalone MongoDB, without a cross-collection transaction requirement.
// Archive completed identities before the balance document grows too large.
// The archive has no TTL: old operations must remain safe to replay.
const maxAccountOperations = 128

type accountOperation struct {
	ID             string `bson:"id"`
	Fingerprint    string `bson:"fingerprint"`
	Response       []byte `bson:"response"`
	EventTimestamp int64  `bson:"eventTimestamp"`
}

type account struct {
	ID          any                 `bson:"_id"`
	Subscriber  string              `bson:"ueId"`
	RatingGroup datatype.Unsigned32 `bson:"ratingGroup"`
	Quota       string              `bson:"quota"`
	Version     *int64              `bson:"chargingVersion,omitempty"`
	Operations  []accountOperation  `bson:"chargingOperations,omitempty"`
}

type Accounts struct {
	policies *mongo.Collection
	balances *mongo.Collection
	archive  *mongo.Collection
}

func NewAccounts(db *mongo.Database) *Accounts {
	return &Accounts{
		policies: chargingdb.DurableCollection(db, "policyData.ues.chargingData"),
		balances: chargingdb.DurableCollection(db, "chf.accounts"),
		archive:  chargingdb.DurableCollection(db, "chf.accountOperations"),
	}
}

// ImportLegacy preserves existing policy-backed balances before serving traffic.
// Old ABMF writers must be stopped first; the owned balance is never overwritten.
func (a *Accounts) ImportLegacy(ctx context.Context) error {
	cursor, err := a.policies.Find(ctx, bson.M{"ratingGroup": bson.M{"$ne": nil}})
	if err != nil {
		return err
	}
	var sources []account
	if err = cursor.All(ctx, &sources); err != nil {
		return err
	}
	for _, source := range sources {
		if err = a.importAccount(ctx, &source); err != nil {
			return err
		}
	}
	return nil
}

func (a *Accounts) importAccount(ctx context.Context, source *account) error {
	_, err := a.balances.UpdateOne(ctx, bson.M{"_id": source.ID},
		bson.M{"$setOnInsert": source}, options.Update().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		// Concurrent first-use imports may race on _id. Other uniqueness
		// violations must still fail if this account was never created.
		return a.balances.FindOne(ctx, bson.M{"_id": source.ID}).Err()
	}
	return err
}

// PCF allocates and reuses rating groups. A policy document's immutable _id
// identifies this generation of account; UE + RG alone is not a permanent key.
// Initial import preserves the old balance and deduplication history, if any.
func (a *Accounts) resolveAccount(ctx context.Context, filter bson.M) (*account, error) {
	source, err := uniqueAccount(ctx, a.policies, filter)
	if err == mongo.ErrNoDocuments {
		// Policy deletion must not prevent a final debit/refund. If RG reuse has
		// left several generations, require a frozen binding instead of guessing.
		return uniqueAccount(ctx, a.balances, filter)
	}
	if err != nil {
		return nil, err
	}
	if err = a.importAccount(ctx, source); err != nil {
		return nil, err
	}
	var balance account
	if err = a.balances.FindOne(ctx, bson.M{"_id": source.ID}).Decode(&balance); err != nil {
		return nil, err
	}
	return &balance, nil
}

func uniqueAccount(ctx context.Context, collection *mongo.Collection, filter bson.M) (*account, error) {
	cursor, err := collection.Find(ctx, filter, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	var matches []account
	if err = cursor.All(ctx, &matches); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, mongo.ErrNoDocuments
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("ambiguous account binding for subscriber and rating group")
	}
	return &matches[0], nil
}

// Completed operations remain replayable even after PCF removes their policy
// or assigns the same rating group to a different policy generation.
func (a *Accounts) completedOperation(ctx context.Context, id, fingerprint string) (
	*charging_datatype.AccountDebitResponse, error,
) {
	var balance account
	err := a.balances.FindOne(ctx, bson.M{"chargingOperations.id": id}).Decode(&balance)
	if err != nil && err != mongo.ErrNoDocuments {
		return nil, err
	}
	for _, operation := range balance.Operations {
		if operation.ID == id {
			return replayAccountOperation(operation, fingerprint)
		}
	}
	var archived accountOperation
	if err = a.archive.FindOne(ctx, bson.M{"_id": id}).Decode(&archived); err != nil {
		return nil, err
	}
	return replayAccountOperation(archived, fingerprint)
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

func (a *Accounts) Apply(ctx context.Context, request *charging_datatype.AccountDebitRequest) (
	*charging_datatype.AccountDebitResponse, error,
) {
	subscriber, id, fingerprint, err := accountIdentity(request)
	if err != nil {
		return nil, err
	}
	if response, replayErr := a.completedOperation(ctx, id, fingerprint); replayErr != mongo.ErrNoDocuments {
		return response, replayErr
	}
	filter := bson.M{"ueId": subscriber, "ratingGroup": request.MultipleServicesCreditControl.RatingGroup}
	resolved, err := a.resolveAccount(ctx, filter)
	if err != nil {
		return nil, err
	}
	// Freeze this binding during CAS retries; policy deletion/reuse cannot
	// redirect a running operation to another account.
	for attempt := 0; attempt < 32; attempt++ {
		var balance account
		if err = a.balances.FindOne(ctx, bson.M{"_id": resolved.ID}).Decode(&balance); err != nil {
			return nil, err
		}
		for _, operation := range balance.Operations {
			if operation.ID == id {
				return replayAccountOperation(operation, fingerprint)
			}
		}
		// Read the balance before its archive. Archival increments chargingVersion,
		// so a concurrent move can neither hide an identity from both reads nor let
		// a stale balance update commit.
		var archived accountOperation
		lookupErr := a.archive.FindOne(ctx, bson.M{"_id": id}).Decode(&archived)
		if lookupErr == nil {
			return replayAccountOperation(archived, fingerprint)
		}
		if lookupErr != mongo.ErrNoDocuments {
			return nil, lookupErr
		}
		if len(balance.Operations) >= maxAccountOperations {
			if err = a.archiveOperations(ctx, balance); err != nil {
				return nil, err
			}
			continue
		}
		quota, parseErr := strconv.ParseInt(balance.Quota, 10, 64)
		if parseErr != nil {
			return nil, parseErr
		}
		response, remaining, calculateErr := calculateAccountResponse(request, quota)
		if calculateErr != nil {
			return nil, calculateErr
		}
		body, encodeErr := json.Marshal(response)
		if encodeErr != nil {
			return nil, encodeErr
		}
		condition := balanceCondition(balance)
		condition["chargingOperations.id"] = bson.M{"$ne": id}
		result, updateErr := a.balances.UpdateOne(ctx, condition, bson.M{
			"$set": bson.M{"quota": strconv.FormatInt(remaining, 10)},
			"$inc": bson.M{"chargingVersion": 1},
			"$push": bson.M{"chargingOperations": accountOperation{
				ID: id, Fingerprint: fingerprint, Response: body,
				EventTimestamp: time.Time(response.EventTimestamp).UnixNano(),
			}},
		})
		if updateErr != nil {
			return nil, updateErr
		}
		if result.MatchedCount == 1 {
			return response, nil
		}
	}
	return nil, fmt.Errorf("account balance changed too often; retry the same operation")
}

func balanceCondition(balance account) bson.M {
	condition := bson.M{"_id": balance.ID, "quota": balance.Quota}
	if balance.Version == nil {
		condition["chargingVersion"] = bson.M{"$exists": false}
	} else {
		condition["chargingVersion"] = *balance.Version
	}
	return condition
}

func replayAccountOperation(
	operation accountOperation, fingerprint string,
) (*charging_datatype.AccountDebitResponse, error) {
	if operation.Fingerprint != fingerprint {
		return nil, ErrConflict
	}
	var response charging_datatype.AccountDebitResponse
	if err := json.Unmarshal(operation.Response, &response); err != nil {
		return nil, err
	}
	// Diameter Time does not implement JSON unmarshalling.
	response.EventTimestamp = datatype.Time(time.Unix(0, operation.EventTimestamp).UTC())
	return &response, nil
}

func (a *Accounts) archiveOperations(ctx context.Context, balance account) error {
	entries := balance.Operations[:len(balance.Operations)/2]
	writes := make([]mongo.WriteModel, 0, len(entries))
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		writes = append(writes, mongo.NewUpdateOneModel().SetFilter(bson.M{
			"_id":         entry.ID,
			"fingerprint": entry.Fingerprint,
		}).SetUpdate(bson.M{"$setOnInsert": entry}).SetUpsert(true))
		ids = append(ids, entry.ID)
	}
	// Persist every replay result first; a crash before the following pull merely
	// leaves duplicate copies, each describing the same completed operation.
	if _, err := a.archive.BulkWrite(ctx, writes); err != nil {
		return err
	}
	_, err := a.balances.UpdateOne(ctx, balanceCondition(balance), bson.M{
		"$pull": bson.M{"chargingOperations": bson.M{"id": bson.M{"$in": ids}}},
		"$inc":  bson.M{"chargingVersion": 1},
	})
	return err
}
