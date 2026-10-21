package processor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
	response, problemDetails := p.ChargingDataUpdate(models.Chf_ConvCharging_ChargingDataRequest{
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

	cdr, err := p.OpenCDR(models.Chf_ConvCharging_ChargingDataRequest{}, ue, "missing-session", true)
	require.Nil(t, cdr)
	require.Error(t, err)
}

// A valid usage report must reach session lookup instead of being rejected for
// omitting RequestedUnit. Use an unknown reference to avoid external rating/CDR I/O.
func TestChargingdataUpdateAcceptsLastOnlineUsageWithoutRequestedUnit(t *testing.T) {
	self := chf_context.GetSelf()
	const supi = "imsi-208930000000952"
	self.UePool.Store(supi, &chf_context.ChfUe{Supi: supi, Cdr: map[string]*cdrType.CHFRecord{}})
	t.Cleanup(func() { self.UePool.Delete(supi) })
	finalTrigger := models.Chf_ConvCharging_Trigger{
		TriggerType:     models.Chf_ConvCharging_TriggerType_FINAL,
		TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
	}
	for _, tc := range []struct {
		name            string
		requestTriggers []models.Chf_ConvCharging_Trigger
		usageTriggers   []models.Chf_ConvCharging_Trigger
		ongoingGroup    bool
	}{
		{name: "implicit last request"},
		{name: "request FINAL", requestTriggers: []models.Chf_ConvCharging_Trigger{finalTrigger}},
		{name: "usage FINAL", usageTriggers: []models.Chf_ConvCharging_Trigger{finalTrigger}},
		{name: "another rating group requests quota", ongoingGroup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := models.Chf_ConvCharging_ChargingDataRequest{
				SubscriberIdentifier: supi,
				Triggers:             tc.requestTriggers,
				MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{{
					RatingGroup: 1,
					UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
						QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
						TotalVolume:              600,
						Triggers:                 tc.usageTriggers,
					}},
				}},
			}
			if tc.ongoingGroup {
				request.MultipleUnitUsage = append(request.MultipleUnitUsage,
					models.Chf_ConvCharging_MultipleUnitUsage{
						RatingGroup:   2,
						RequestedUnit: &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 1000},
						UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
							QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
						}},
					})
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			(&Processor{}).HandleChargingdataUpdate(ctx, request, "unknown-charging-data-ref")
			require.Equal(t, http.StatusNotFound, recorder.Code)
			require.JSONEq(t, `{
				"title":"Context Not Found", "status":404,
				"detail":"ChargingDataRef[unknown-charging-data-ref] not found", "cause":"CONTEXT_NOT_FOUND"
			}`, recorder.Body.String())
		})
	}
}
