package util

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/openapi/models"
)

func TestValidateChargingDataCreateRequest(t *testing.T) {
	testCases := []struct {
		description    string
		chargingData   models.Chf_ConvCharging_ChargingDataRequest
		expectProblem  bool
		expectedStatus int
		expectedCause  string
	}{
		{
			description: "TC1: missing nFConsumerIdentification should fail",
			chargingData: models.Chf_ConvCharging_ChargingDataRequest{
				SubscriberIdentifier:     "imsi-208930000000003",
				ChargingId:               1,
				InvocationSequenceNumber: 1,
			},
			expectProblem:  true,
			expectedStatus: http.StatusBadRequest,
			expectedCause:  "MANDATORY_IE_MISSING",
		},
		{
			description: "TC2: present nFConsumerIdentification should pass",
			chargingData: models.Chf_ConvCharging_ChargingDataRequest{
				SubscriberIdentifier: "imsi-208930000000003",
				ChargingId:           1,
				NfConsumerIdentification: &models.Chf_ConvCharging_NFIdentification{
					NFName:            "amf",
					NodeFunctionality: "SMF",
				},
				InvocationSequenceNumber: 1,
			},
			expectProblem: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			problem := ValidateChargingDataCreateRequest(tc.chargingData)
			if tc.expectProblem {
				require.NotNil(t, problem)
				require.Equal(t, tc.expectedStatus, int(problem.Status))
				require.Equal(t, tc.expectedCause, problem.Cause)
				return
			}

			require.Nil(t, problem)
		})
	}
}

func TestValidateOnlineChargingRequestedUnit(t *testing.T) {
	testCases := []struct {
		description    string
		chargingData   models.Chf_ConvCharging_ChargingDataRequest
		expectProblem  bool
		expectedStatus int
		expectedCause  string
	}{
		{
			description: "TC1: online charging without requestedUnit should fail",
			chargingData: models.Chf_ConvCharging_ChargingDataRequest{
				MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{
					{
						UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{
							{QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING},
						},
						RequestedUnit: nil,
					},
				},
			},
			expectProblem:  true,
			expectedStatus: http.StatusBadRequest,
			expectedCause:  "",
		},
		{
			description: "TC2: online charging with requestedUnit should pass",
			chargingData: models.Chf_ConvCharging_ChargingDataRequest{
				MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{
					{
						UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{
							{QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING},
						},
						RequestedUnit: &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100},
					},
				},
			},
			expectProblem: false,
		},
		{
			description: "TC3: non-online charging without requestedUnit should pass",
			chargingData: models.Chf_ConvCharging_ChargingDataRequest{
				MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{
					{
						UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{
							{QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_QUOTA_MANAGEMENT_SUSPENDED},
						},
						RequestedUnit: nil,
					},
				},
			},
			expectProblem: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			problem := ValidateOnlineChargingRequestedUnit(tc.chargingData)
			if tc.expectProblem {
				require.NotNil(t, problem)
				require.Equal(t, tc.expectedStatus, int(problem.Status))
				require.Equal(t, tc.expectedCause, problem.Cause)
				return
			}

			require.Nil(t, problem)
		})
	}
}
