package context

import (
	"time"
)

type RequestSubType int
type DestinationIDType int
type SubscriptionIdType int
type CCUnitType int
type ChargeReasonCode int

const (
	UNKNOWN ChargeReasonCode = iota
	USAGE
	COMMUNICATIONATTEMPTCHARGE
	SETUPCHARGE
	ADDONCHARGE
)

const (
	TIME CCUnitType = iota
	MONEY
	TOTALOCTETS
	INPUTOCTETS
	OUTPUTOCTETS
	SERVICESPECIFICUNITS
)

const (
	REQ_SUBTYPE_AOC RequestSubType = iota
	REQ_SUBTYPE_RESERVE
	REQ_SUBTYPE_DEBIT
	REQ_SUBTYPE_RELEASE
)

const (
	Destination_Number DestinationIDType = iota
	Destination_APN
	Destination_URL
	Destination_EmailAddress
	Destination_PrivateID
)

const (
	END_USER_E164 SubscriptionIdType = iota
	END_USER_IMSI
	END_USER_SIP_URI
	END_USER_NAI
	END_USER_PRIVATE
)

type MonetaryTariffAfterValidUnits struct {
	CurrencyCode uint32
	ScaleFactor  *ScaleFactor
	RateElement  *RateElement
}

type NextTariff struct {
	CurrencyCode uint32
	ScaleFactor  *ScaleFactor
	RateElement  *RateElement
}

type UnitCost struct {
	ValueDigits int64
	Exponent    int32
}
type UnitValue struct {
	ValueDigits int64
	Exponent    int32
}
type RateElement struct {
	CCUnitType         *CCUnitType
	ChargeReasonCode   *ChargeReasonCode
	UnitValue          *UnitValue
	UnitCost           *UnitCost
	UnitQuotaThreshold uint32
}

type ScaleFactor struct {
	ValueDigits int64
	Exponent    int32
}

type CurrentTariff struct {
	CurrencyCode uint32
	ScaleFactor  *ScaleFactor
	RateElement  *RateElement
}

type ImpactOnCounter struct {
	CounterID    uint32
	CounterValue int32
}

type ServiceInformation struct {
	SubscriptionId *SubscriptionId
	//TODO: xxx-infomation
}

type DestinationID struct {
	DestinationIDType *DestinationIDType
	DestinationData   string
	CounterExpiryDate time.Time
}

type ServiceRating struct {
	ServiceIdentifier  string
	DestinationID      *DestinationID
	ServiceInformation *ServiceInformation
	// Extension          Extension
	RequestSubType                 *RequestSubType
	Price                          uint32
	BillingInfo                    string
	ImpactOnCounter                *ImpactOnCounter
	RequestedUnits                 uint32
	ConsumedUnits                  uint32
	ConsumedUnitsAfterTariffSwitch uint32
	TariffSwitchTime               uint32
	CurrentTariff                  *CurrentTariff
	NextTariff                     *NextTariff
	ExpiryTime                     uint32
	ValidUnits                     uint32
	MonetaryTariffAfterValidUnits  *MonetaryTariffAfterValidUnits
	MonetaryQuota                  uint32
	MinimalRequestedUnits          uint32
	AllowedUnits                   uint32
}

type SubscriptionId struct {
	SubscriptionIdData string
	SubscriptionIdType *SubscriptionIdType
}

type ServiceUsageResponse struct {
	SessionID     int
	ServiceRating *ServiceRating
}
type ServiceUsageRequest struct {
	SessionID      int
	ActualTime     time.Time
	BeginTime      time.Time
	SubscriptionId *SubscriptionId
	ServiceRating  *ServiceRating
}
