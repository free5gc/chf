package context

import (
	"slices"

	"github.com/free5gc/openapi/models"
)

const ServiceNameNsmfCallback models.Nrf_NFMgmt_ServiceName = "nsmf-callback"

var servicePolicies = map[models.Nrf_NFMgmt_ServiceName][]models.Nrf_NFMgmt_NFType{
	models.Nrf_NFMgmt_ServiceName_NCHF_CONVERGEDCHARGING: {
		models.Nrf_NFMgmt_NFType_SMF,
		models.Nrf_NFMgmt_NFType_SMSF,
		models.Nrf_NFMgmt_NFType_AMF,
		models.Nrf_NFMgmt_NFType_NEF,
		models.Nrf_NFMgmt_NFType_CEF,
		models.Nrf_NFMgmt_NFType_5_G_DDNMF,
	},
}

func AllowedNfTypesForService(serviceName models.Nrf_NFMgmt_ServiceName) (
	[]models.Nrf_NFMgmt_NFType, bool,
) {
	allowed, known := servicePolicies[serviceName]
	return slices.Clone(allowed), known
}
