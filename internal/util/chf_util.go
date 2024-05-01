package util

import (
	// "github.com/free5gc/openapi/Nchf_ConvergedCharging"
	Nchf_ConvergedCharging "github.com/free5gc/openapi-r17/chf/ConvergedCharging"
)

func GetNchfChargingNotificationCallbackClient() *Nchf_ConvergedCharging.APIClient {
	configuration := Nchf_ConvergedCharging.NewConfiguration()
	client := Nchf_ConvergedCharging.NewAPIClient(configuration)
	return client
}
