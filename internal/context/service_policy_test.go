package context

import (
	"testing"

	"github.com/free5gc/openapi/models"
)

func TestAllowedNfTypesForServiceCoversCHFServices(t *testing.T) {
	services := []models.Nrf_NFMgmt_ServiceName{
		models.Nrf_NFMgmt_ServiceName_NCHF_CONVERGEDCHARGING,
		models.Nrf_NFMgmt_ServiceName_NCHF_OFFLINEONLYCHARGING,
		models.Nrf_NFMgmt_ServiceName_NCHF_SPENDINGLIMITCONTROL,
	}
	for _, service := range services {
		allowed, known := AllowedNfTypesForService(service)
		if !known {
			t.Errorf("service %q has no AllowedNfTypes policy", service)
			continue
		}
		if len(allowed) == 0 {
			t.Errorf("service %q has an empty AllowedNfTypes policy", service)
		}
	}
}
