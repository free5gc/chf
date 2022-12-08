package rating

import (
	"math"

	"github.com/free5gc/TarrifUtil/tarrifType"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
)

func ServiceUsageRetrieval(serviceUsage tarrifType.ServiceUsageRequest) (tarrifType.ServiceUsageResponse, *models.ProblemDetails, bool) {
	lastgrantedquota := false

	unitCost := (serviceUsage.ServiceRating.CurrentTariff.RateElement.UnitCost.ValueDigits) * int64(math.Pow10(int(serviceUsage.ServiceRating.CurrentTariff.RateElement.UnitCost.Exponent)))
	monetaryCost := int64(serviceUsage.ServiceRating.ConsumedUnits) * unitCost
	monetaryRequest := int64(serviceUsage.ServiceRating.RequestedUnits) * unitCost

	rsp := tarrifType.ServiceUsageResponse{
		SessionID: serviceUsage.SessionID,
		ServiceRating: &tarrifType.ServiceRating{
			Price:         uint32(monetaryCost),
			MonetaryQuota: serviceUsage.ServiceRating.MonetaryQuota,
		},
	}

	if serviceUsage.ServiceRating.RequestSubType.Value == tarrifType.REQ_SUBTYPE_DEBIT {
		logger.ChargingdataPostLog.Warnf("Out of Monetary Quota, Debit mode")
		rsp.ServiceRating.AllowedUnits = 0
		return rsp, nil, lastgrantedquota
	} else if serviceUsage.ServiceRating.RequestSubType.Value == tarrifType.REQ_SUBTYPE_RESERVE {
		if monetaryCost < int64(serviceUsage.ServiceRating.MonetaryQuota) {
			monetaryRemain := int64(serviceUsage.ServiceRating.MonetaryQuota) - monetaryCost
			if (monetaryRemain - monetaryRequest) > 0 {
				rsp.ServiceRating.AllowedUnits = serviceUsage.ServiceRating.RequestedUnits
			} else {
				rsp.ServiceRating.AllowedUnits = uint32(monetaryRemain / unitCost)
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
