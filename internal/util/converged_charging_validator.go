package util

import (
	"fmt"

	"github.com/free5gc/openapi"
	"github.com/free5gc/openapi/models"
)

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
