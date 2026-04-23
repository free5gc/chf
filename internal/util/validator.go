package util

import (
	"fmt"
	"net/http"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi"
	"github.com/free5gc/openapi/models"
)

func ValidateChargingDataCreateRequest(
	chargingData models.ChfConvergedChargingChargingDataRequest,
) *models.ProblemDetails {
	if chargingData.NfConsumerIdentification == nil {
		detail := "nFConsumerIdentification is not presented"
		logger.ChargingdataPostLog.Warnln(detail)
		return &models.ProblemDetails{
			Title:  "Malformed request syntax",
			Status: http.StatusBadRequest,
			Detail: detail,
			Cause:  "MANDATORY_IE_MISSING",
		}
	}

	return nil
}

func ValidateOnlineChargingRequestedUnit(
	chargingData models.ChfConvergedChargingChargingDataRequest,
) *models.ProblemDetails {
	for idx, unitUsage := range chargingData.MultipleUnitUsage {
		onlineCharging := false
		for _, usedUnit := range unitUsage.UsedUnitContainer {
			if usedUnit.QuotaManagementIndicator == models.QuotaManagementIndicator_ONLINE_CHARGING {
				onlineCharging = true
				break
			}
		}

		if onlineCharging && unitUsage.RequestedUnit == nil {
			return openapi.ProblemDetailsMalformedReqSyntax(
				fmt.Sprintf(
					"multipleUnitUsage[%d].requestedUnit is required when quotaManagementIndicator is ONLINE_CHARGING",
					idx,
				),
			)
		}
	}

	return nil
}
