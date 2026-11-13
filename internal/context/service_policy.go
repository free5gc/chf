package context

import (
	"slices"

	"github.com/free5gc/openapi/models"
)

var servicePolicies = map[models.Nrf_NFMgmt_ServiceName][]models.Nrf_NFMgmt_NFType{
	models.Nrf_NFMgmt_ServiceName_NCHF_CONVERGEDCHARGING: {
		models.Nrf_NFMgmt_NFType_SMF,
		models.Nrf_NFMgmt_NFType_SMSF,
		models.Nrf_NFMgmt_NFType_AMF,
		models.Nrf_NFMgmt_NFType_NEF,
		models.Nrf_NFMgmt_NFType_CEF,
		models.Nrf_NFMgmt_NFType_5_G_DDNMF,
	},
	// TS 32.291 / TS 32.255: offline-only charging is requested by the SMF (CTF).
	models.Nrf_NFMgmt_ServiceName_NCHF_OFFLINEONLYCHARGING: {
		models.Nrf_NFMgmt_NFType_SMF,
	},
	// TS 29.594 §4.1: the NF service consumer of Nchf_SpendingLimitControl is the PCF.
	models.Nrf_NFMgmt_ServiceName_NCHF_SPENDINGLIMITCONTROL: {
		models.Nrf_NFMgmt_NFType_PCF,
	},
}

func AllowedNfTypesForService(serviceName models.Nrf_NFMgmt_ServiceName) (
	[]models.Nrf_NFMgmt_NFType, bool,
) {
	allowed, known := servicePolicies[serviceName]
	return slices.Clone(allowed), known
}
