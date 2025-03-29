package util

import (
	Nchf_ConvergedCharging "github.com/free5gc/openapi/chf/ConvergedCharging"
	"github.com/free5gc/openapi/models"
)

func GetNchfChargingNotificationCallbackClient() *Nchf_ConvergedCharging.APIClient {
	configuration := Nchf_ConvergedCharging.NewConfiguration()
	client := Nchf_ConvergedCharging.NewAPIClient(configuration)
	return client
}

func SnssaiModelsToExtSnssai(snssai models.Snssai) models.ExtSnssai {
	ExtStssai := models.ExtSnssai{
		Sst: snssai.Sst,
		Sd:  snssai.Sd,
	}
	return ExtStssai
}
