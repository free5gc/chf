package rating

import (
	"math"

	"github.com/free5gc/TarrifUtil/tarrifType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
)

func ServiceUsageRetrieval(serviceUsage tarrifType.ServiceUsageRequest) (tarrifType.ServiceUsageResponse, *models.ProblemDetails, bool) {
	self := chf_context.CHF_Self()
	lastgrantedquota := false

	unitCost := self.Tarrif.RateElement.UnitCost.ValueDigits * int64(math.Pow10(self.Tarrif.RateElement.UnitCost.Exponent))
	monetaryCost := int64(serviceUsage.ServiceRating.ConsumedUnits) * unitCost
	monetaryRequest := int64(serviceUsage.ServiceRating.RequestedUnits) * unitCost

	rsp := tarrifType.ServiceUsageResponse{
		SessionID: serviceUsage.SessionID,
		ServiceRating: &tarrifType.ServiceRating{
			CurrentTariff: &self.Tarrif,
			Price:         uint32(monetaryCost),
		},
	}

	if serviceUsage.ServiceRating.RequestSubType.Value == tarrifType.REQ_SUBTYPE_DEBIT {
		logger.ChargingdataPostLog.Warnf("Debit mode")

		return rsp, nil, lastgrantedquota
	} else if serviceUsage.ServiceRating.RequestSubType.Value == tarrifType.REQ_SUBTYPE_RESERVE {
		if monetaryCost < int64(serviceUsage.ServiceRating.MonetaryQuota) {
			monetaryRemain := int64(serviceUsage.ServiceRating.MonetaryQuota) - monetaryCost - monetaryRequest
			if monetaryRemain > 0 {
				rsp.ServiceRating.AllowedUnits = serviceUsage.ServiceRating.RequestedUnits
			} else {
				rsp.ServiceRating.AllowedUnits = uint32((int64(serviceUsage.ServiceRating.MonetaryQuota) - monetaryCost) / unitCost)
				logger.ChargingdataPostLog.Warn("Last granted Quota")
				lastgrantedquota = true
			}

			// MonetaryCost = (used quota + reserve quota) * unitCost
			rsp.ServiceRating.Price += uint32(int64(rsp.ServiceRating.AllowedUnits) * unitCost)
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
