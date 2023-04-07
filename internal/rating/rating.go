package rating

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/free5gc/RatingUtil/dataType"
	rate_datatype "github.com/free5gc/RatingUtil/dataType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/mongoapi"
	"go.mongodb.org/mongo-driver/bson"
)

const chargingDataColl = "chargingData"

func ServiceUsageRetrieval(serviceUsage rate_datatype.ServiceUsageRequest) (rate_datatype.ServiceUsageResponse, *models.ProblemDetails, bool) {
	lastgrantedquota := false

	unitCost := (serviceUsage.ServiceRating.MonetaryTariff.RateElement.UnitCost.ValueDigits) * datatype.Integer64(math.Pow10(int(serviceUsage.ServiceRating.MonetaryTariff.RateElement.UnitCost.Exponent)))
	monetaryCost := datatype.Integer64(serviceUsage.ServiceRating.ConsumedUnits) * unitCost
	monetaryRequest := datatype.Integer64(serviceUsage.ServiceRating.RequestedUnits) * unitCost

	logger.ChargingdataPostLog.Tracef("Cost per Byte[%d]", unitCost)
	rsp := dataType.ServiceUsageResponse{
		SessionId: serviceUsage.SessionId,
		ServiceRating: &dataType.ServiceRating{
			Price:         datatype.Unsigned32(monetaryCost),
			MonetaryQuota: serviceUsage.ServiceRating.MonetaryQuota,
		},
	}

	if serviceUsage.ServiceRating.RequestSubType == dataType.REQ_SUBTYPE_DEBIT {
		logger.ChargingdataPostLog.Warnf("Out of Monetary Quota, Debit mode")
		rsp.ServiceRating.AllowedUnits = 0
		return rsp, nil, lastgrantedquota
	} else if serviceUsage.ServiceRating.RequestSubType == dataType.REQ_SUBTYPE_RESERVE {
		if monetaryCost < datatype.Integer64(serviceUsage.ServiceRating.MonetaryQuota) {
			monetaryRemain := datatype.Integer64(serviceUsage.ServiceRating.MonetaryQuota) - monetaryCost
			if (monetaryRemain - monetaryRequest) > 0 {
				rsp.ServiceRating.AllowedUnits = serviceUsage.ServiceRating.RequestedUnits
			} else {
				rsp.ServiceRating.AllowedUnits = datatype.Unsigned32(monetaryRemain / unitCost)
				logger.ChargingdataPostLog.Warn("Last granted Quota")
				lastgrantedquota = true
			}
		} else {
			logger.ChargingdataPostLog.Warn("Out of Monetary Quota")
			rsp.ServiceRating.AllowedUnits = 0
			return rsp, nil, lastgrantedquota
		}
	} else {
		logger.ChargingdataPostLog.Warnf("Unsupport RequestSubType")
	}

	return rsp, nil, lastgrantedquota
}

func buildTaffif(unitCostStr string) rate_datatype.MonetaryTariff {
	// unitCost
	unitCost := rate_datatype.UnitCost{}
	dotPos := strings.Index(unitCostStr, ".")
	if dotPos == -1 {
		unitCost.Exponent = 0
		if digit, err := strconv.Atoi(unitCostStr); err == nil {
			unitCost.ValueDigits = datatype.Integer64(digit)
		}
	} else {
		if digit, err := strconv.Atoi(strings.Replace(unitCostStr, ".", "", -1)); err == nil {
			unitCost.ValueDigits = datatype.Integer64(digit)
		}
		unitCost.Exponent = datatype.Integer32(len(unitCostStr) - dotPos - 1)
	}

	return rate_datatype.MonetaryTariff{
		RateElement: &dataType.RateElement{
			UnitCost: &unitCost,
		},
	}
}

func BuildServiceUsageRequest(chargingData models.ChargingDataRequest, unitUsage models.MultipleUnitUsage) dataType.ServiceUsageRequest {
	var subscriberIdentifier dataType.SubscriptionId

	self := chf_context.CHF_Self()
	sessionid, err := self.RatingSessionGenerator.Allocate()
	if err != nil {
		logger.ChargingdataPostLog.Errorf("Rating Session Allocate err: %+v", err)
	}

	supi := chargingData.SubscriberIdentifier
	supiType := strings.Split(supi, "-")[0]

	switch supiType {
	case "imsi":
		subscriberIdentifier = dataType.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_IMSI,
			SubscriptionIdData: datatype.UTF8String(supi[5:]),
		}
	case "nai":
		subscriberIdentifier = dataType.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	case "gci":
		subscriberIdentifier = dataType.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	case "gli":
		subscriberIdentifier = dataType.SubscriptionId{
			SubscriptionIdType: dataType.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	}

	// Rating for each rating group
	var totalUsaedUnit uint32
	for _, useduint := range unitUsage.UsedUnitContainer {
		if useduint.QuotaManagementIndicator == models.QuotaManagementIndicator_OFFLINE_CHARGING {
			continue
		}

		totalUsaedUnit += uint32(useduint.TotalVolume)
	}

	ue, ok := self.ChfUeFindBySupi(supi)
	if ok {
		ue.AccumulateUsage.TotalVolume += int32(totalUsaedUnit)
		logger.ChargingdataPostLog.Warnf("UE's[%s] accumulate data usage %d", supi, ue.AccumulateUsage.TotalVolume)
	}

	filter := bson.M{"ueId": chargingData.SubscriberIdentifier, "ratingGroup": unitUsage.RatingGroup}
	chargingInterface, err := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)
	if err != nil {
		logger.ChargingdataPostLog.Errorf("Get quota error: %+v", err)
	}

	// workaround
	// type reading from mongoDB is not stabe
	// i.g. chargingInterface["quota"] may be int, float...
	// 		tarrifInterface["rateelement"] may be tarrifInterface["rateElement"]
	quota := uint32(0)
	switch value := chargingInterface["quota"].(type) {
	case int:
		quota = uint32(value)
	case int32:
		quota = uint32(value)
	case int64:
		quota = uint32(value)
	case float64:
		quota = uint32(value)
	default:
		logger.ChargingdataPostLog.Errorf("Get quota error: do not belong to int or float, type:%T", chargingInterface["quota"])
	}

	unitCost := chargingInterface["unitCost"].(string)
	tarrif := buildTaffif(unitCost)

	ServiceUsageRequest := dataType.ServiceUsageRequest{
		SessionId:      datatype.UTF8String(strconv.Itoa(int(sessionid))),
		SubscriptionId: &subscriberIdentifier,
		ActualTime:     datatype.Time(time.Now()),
		ServiceRating: &dataType.ServiceRating{
			RequestedUnits: datatype.Unsigned32(unitUsage.RequestedUnit.TotalVolume),
			ConsumedUnits:  datatype.Unsigned32(totalUsaedUnit),
			RequestSubType: rate_datatype.REQ_SUBTYPE_RESERVE,
			MonetaryTariff: &tarrif,
			MonetaryQuota:  datatype.Unsigned32(quota),
		},
	}
	if quota == 0 {
		ServiceUsageRequest.ServiceRating.RequestSubType = dataType.REQ_SUBTYPE_DEBIT
	}

	for _, trigger := range chargingData.Triggers {
		if trigger.TriggerType == models.TriggerType_FINAL {
			ServiceUsageRequest.ServiceRating.RequestSubType = dataType.REQ_SUBTYPE_DEBIT
		}
	}
	return ServiceUsageRequest
}
