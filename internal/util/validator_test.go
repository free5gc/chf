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
		chargingData   models.ChfConvergedChargingChargingDataRequest
		expectProblem  bool
		expectedStatus int
		expectedCause  string
	}{
		{
			description: "TC1: missing nFConsumerIdentification should fail",
			chargingData: models.ChfConvergedChargingChargingDataRequest{
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
			chargingData: models.ChfConvergedChargingChargingDataRequest{
				SubscriberIdentifier: "imsi-208930000000003",
				ChargingId:           1,
				NfConsumerIdentification: &models.ChfConvergedChargingNfIdentification{
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
		chargingData   models.ChfConvergedChargingChargingDataRequest
		expectProblem  bool
		expectedStatus int
		expectedCause  string
	}{
		{
			description: "TC1: online charging without requestedUnit should fail",
			chargingData: models.ChfConvergedChargingChargingDataRequest{
				MultipleUnitUsage: []models.ChfConvergedChargingMultipleUnitUsage{
					{
						UsedUnitContainer: []models.ChfConvergedChargingUsedUnitContainer{
							{QuotaManagementIndicator: models.QuotaManagementIndicator_ONLINE_CHARGING},
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
			chargingData: models.ChfConvergedChargingChargingDataRequest{
				MultipleUnitUsage: []models.ChfConvergedChargingMultipleUnitUsage{
					{
						UsedUnitContainer: []models.ChfConvergedChargingUsedUnitContainer{
							{QuotaManagementIndicator: models.QuotaManagementIndicator_ONLINE_CHARGING},
						},
						RequestedUnit: &models.RequestedUnit{TotalVolume: 100},
					},
				},
			},
			expectProblem: false,
		},
		{
			description: "TC3: non-online charging without requestedUnit should pass",
			chargingData: models.ChfConvergedChargingChargingDataRequest{
				MultipleUnitUsage: []models.ChfConvergedChargingMultipleUnitUsage{
					{
						UsedUnitContainer: []models.ChfConvergedChargingUsedUnitContainer{
							{QuotaManagementIndicator: models.QuotaManagementIndicator_QUOTA_MANAGEMENT_SUSPENDED},
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
