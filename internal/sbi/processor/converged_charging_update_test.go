package processor

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/chf/cdr/cdrType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

func TestChargingDataUpdateRejectsUnknownChargingDataRef(t *testing.T) {
	self := chf_context.GetSelf()
	supi := "imsi-208930000000950"

	ue := &chf_context.ChfUe{
		Supi: supi,
		Cdr: map[string]*cdrType.CHFRecord{
			"valid-session": {
				ChargingFunctionRecord: &cdrType.ChargingRecord{},
			},
		},
		Records: []*cdrType.CHFRecord{
			{ChargingFunctionRecord: &cdrType.ChargingRecord{}},
			{ChargingFunctionRecord: &cdrType.ChargingRecord{}},
		},
	}

	self.UePool.Store(supi, ue)
	t.Cleanup(func() {
		self.UePool.Delete(supi)
	})

	p := &Processor{}
	response, problemDetails := p.ChargingDataUpdate(models.ChfConvergedChargingChargingDataRequest{
		SubscriberIdentifier: supi,
	}, "does-not-exist-partial")

	require.Nil(t, response)
	require.NotNil(t, problemDetails)
	require.Equal(t, http.StatusNotFound, int(problemDetails.Status))
	require.Equal(t, "CONTEXT_NOT_FOUND", problemDetails.Cause)
}

func TestOpenCDRPartialRecordReturnsErrorWhenSessionMissing(t *testing.T) {
	p := &Processor{}

	ue := &chf_context.ChfUe{
		Supi: "imsi-208930000000951",
		Cdr:  map[string]*cdrType.CHFRecord{},
	}

	cdr, err := p.OpenCDR(models.ChfConvergedChargingChargingDataRequest{}, ue, "missing-session", true)
	require.Nil(t, cdr)
	require.Error(t, err)
}
