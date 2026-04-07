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
