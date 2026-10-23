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

// Steps follow original usage-entry indices, including repeated RGs. A later
// step can depend on the actual credit granted by an earlier account answer.
type SettlementPlan struct {
	Version  int               `bson:"version"`
	Steps    map[string][]byte `bson:"steps"`
	Outcomes map[string][]byte `bson:"outcomes"`
}

type QuotaState struct {
	Known      bool
	Reserved   int64
	RatingType charging_datatype.RequestSubType
	UnitCost   uint32
}

type RatingExchange struct {
	Request  charging_datatype.ServiceUsageRequest
	Response charging_datatype.ServiceUsageResponse
}

type SettlementStep struct {
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
	PartialRecord        bool
}

// An outcome records the account answer and proposed quota state. It does not
// prove that live or future durable quota state has been updated.
type SettlementOutcome struct {
	After           QuotaState
	AccountResponse *charging_datatype.AccountDebitResponse
	UnitInformation models.Chf_ConvCharging_MultipleUnitInformation
}

func (j *MongoJournal) SaveSettlementStep(ctx context.Context, record *Record, step *SettlementStep) error {
	return j.saveSettlementSnapshot(ctx, record, step.UsageIndex, "steps", step)
}

func (j *MongoJournal) SaveSettlementOutcome(
	ctx context.Context, record *Record, index int, outcome *SettlementOutcome,
) error {
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
	filter := bson.M{
		"_id": record.ID, "state": Processing,
		"$or": bson.A{bson.M{field: bson.M{"$exists": false}}, bson.M{field: body}},
	}
	if kind == "outcomes" {
		filter["settlementPlan.steps."+key] = bson.M{"$exists": true}
	}
	result, err := j.requests.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		field: body, "settlementPlan.version": 1, "updatedAt": time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		stored, loadErr := j.load(ctx, record.ID)
		if loadErr != nil {
			return loadErr
		}
		if stored.State != Processing {
			return ErrPending
		}
		return ErrConflict
	}
	return nil
}
