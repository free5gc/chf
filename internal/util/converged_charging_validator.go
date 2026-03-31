package util

import (
	"net/http"

	"github.com/free5gc/chf/internal/logger"
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
