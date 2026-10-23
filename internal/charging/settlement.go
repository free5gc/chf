package charging

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/openapi/models"
)

// QuotaKey identifies one independently granted UPF quota within a resource.
// An empty UPFID represents a legacy single-UPF quota for that resource and RG.
type QuotaKey struct {
	ChargingDataRef string
	RatingGroup     int32
	UPFID           string
}

// Steps follow original usage-entry indices. Repeated entries for the same
// quota owner can depend on an earlier entry's actual account grant.
type SettlementPlan struct {
	Steps    map[string][]byte `bson:"steps"`
	Outcomes map[string][]byte `bson:"outcomes"`
}

type QuotaState struct {
	Known      bool
	Reserved   int64
	RatingType charging_datatype.RequestSubType
	UnitCost   uint32
	Exhausted  bool
	Closed     bool
}

type RatingExchange struct {
	Request  charging_datatype.ServiceUsageRequest
	Response charging_datatype.ServiceUsageResponse
}

type SettlementStep struct {
	QuotaKey    QuotaKey
	UsageIndex  int
	RatingGroup int32
	UPFID       string
	Before      QuotaState
	// Planned excludes ABMF granted units when AddGrantedUnits is true.
	Planned              QuotaState
	AddGrantedUnits      bool
	Ratings              []RatingExchange
	AccountBinding       []byte
	AccountRequest       *charging_datatype.AccountDebitRequest
	UnitInformation      models.Chf_ConvCharging_MultipleUnitInformation
	FinalUnitInformation *models.Chf_ConvCharging_MultipleUnitInformation
	ThresholdRate        float32
	AdjustThreshold      bool
	PartialRecord        bool
}

// An outcome records the account answer and proposed quota state. Only the
// session document's quotaApplied marker proves that durable state was updated.
type SettlementOutcome struct {
	QuotaKey        QuotaKey
	After           QuotaState
	AccountResponse *charging_datatype.AccountDebitResponse
	UnitInformation models.Chf_ConvCharging_MultipleUnitInformation
}

func (j *MongoJournal) SaveSettlementStep(ctx context.Context, record *Record, step *SettlementStep) error {
	if step.QuotaKey.ChargingDataRef != record.Session || step.QuotaKey.RatingGroup != step.RatingGroup ||
		step.QuotaKey.UPFID != step.UPFID {
		return fmt.Errorf("settlement step has inconsistent quota ownership")
	}
	return j.saveSettlementSnapshot(ctx, record, step.UsageIndex, "steps", step)
}

func (j *MongoJournal) SaveSettlementOutcome(
	ctx context.Context, record *Record, index int, outcome *SettlementOutcome,
) error {
	if outcome.QuotaKey.ChargingDataRef != record.Session {
		return fmt.Errorf("settlement outcome belongs to a different charging resource")
	}
	return j.saveSettlementSnapshot(ctx, record, index, "outcomes", outcome)
}

func (j *MongoJournal) saveSettlementSnapshot(
	ctx context.Context, record *Record, index int, kind string, value any,
) error {
	if index < 0 {
		return fmt.Errorf("negative settlement usage index")
	}
	body, err := chargingdb.EncodeSnapshot(value)
	if err != nil {
		return err
	}
	key := strconv.Itoa(index)
	field := "settlementPlan." + kind + "." + key
	filter := requestLeaseFilter(record)
	filter["$or"] = bson.A{bson.M{field: bson.M{"$exists": false}}, bson.M{field: body}}
	if kind == "outcomes" {
		filter["settlementPlan.steps."+key] = bson.M{"$exists": true}
	}
	result, err := j.requests.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		field: body, "updatedAt": time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		stored, loadErr := j.load(ctx, record.ID)
		if loadErr != nil {
			return loadErr
		}
		if stored.State != Processing || stored.Owner != record.Owner || !stored.LeaseUntil.After(time.Now().UTC()) {
			return ErrPending
		}
		return ErrConflict
	}
	if record.localJournal == j && record.localPlan != nil {
		if kind == "steps" {
			record.localPlan.Steps[key] = body
		} else {
			record.localPlan.Outcomes[key] = body
		}
	}
	return nil
}
