package cdrConvert

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/openapi/models"
)

func TestPlmnIdToCdr(t *testing.T) {
	testCases := []struct {
		description string
		plmn        models.PlmnId
		expectError bool
	}{
		{
			description: "TC1: valid 3-digit MNC",
			plmn: models.PlmnId{
				Mcc: "208",
				Mnc: "930",
			},
			expectError: false,
		},
		{
			description: "TC2: valid 2-digit MNC",
			plmn: models.PlmnId{
				Mcc: "208",
				Mnc: "93",
			},
			expectError: false,
		},
		{
			description: "TC3: short MCC should fail",
			plmn: models.PlmnId{
				Mcc: "2",
				Mnc: "93",
			},
			expectError: true,
		},
		{
			description: "TC4: short MNC should fail",
			plmn: models.PlmnId{
				Mcc: "208",
				Mnc: "9",
			},
			expectError: true,
		},
		{
			description: "TC5: non-digit MCC should fail",
			plmn: models.PlmnId{
				Mcc: "2a8",
				Mnc: "93",
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			cdrPlmn, err := PlmnIdToCdr(tc.plmn)
			if tc.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Len(t, cdrPlmn.Value, 3)
		})
	}
}
