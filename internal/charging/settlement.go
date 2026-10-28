package charging

import (
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
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
	AccountRequest       *charging_datatype.AccountDebitRequest
	UnitInformation      models.Chf_ConvCharging_MultipleUnitInformation
	FinalUnitInformation *models.Chf_ConvCharging_MultipleUnitInformation
	ThresholdRate        float32
	AdjustThreshold      bool
	PartialRecord        bool
}

// An outcome retains the acknowledged account answer for retries in this process.
type SettlementOutcome struct {
	QuotaKey        QuotaKey
	After           QuotaState
	AccountResponse *charging_datatype.AccountDebitResponse
	UnitInformation models.Chf_ConvCharging_MultipleUnitInformation
}
