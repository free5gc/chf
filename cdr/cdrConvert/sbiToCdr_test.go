package cdrConvert

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/chf/cdr/asn"
	"github.com/free5gc/chf/cdr/cdrType"
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

func TestUsedUnitContainerTriggerTimestamp(t *testing.T) {
	utc := time.Date(2026, 11, 4, 23, 59, 30, 0, time.UTC)
	cet := time.Date(2026, 11, 4, 23, 59, 30, 0, time.FixedZone("CET", 3600))
	containers := UsedUnitContainerToCdr([]models.Chf_ConvCharging_UsedUnitContainer{
		{LocalSequenceNumber: 1, TotalVolume: 1000, TriggerTimestamp: &utc},
		{LocalSequenceNumber: 2, TotalVolume: 2000, TriggerTimestamp: &cet},
		{LocalSequenceNumber: 3, TotalVolume: 3000},
	})
	require.Len(t, containers, 3)
	require.Nil(t, containers[2].TriggerTimeStamp, "a container without a trigger timestamp gets none")

	// YYMMDDhhmmssShhmm (TS 32.298), read back after encoding and decoding
	// the CDR container.
	want := []asn.OctetString{
		{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x00, 0x00},
		{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x01, 0x00},
	}
	for i, w := range want {
		encoded, err := asn.BerMarshalWithParams(&containers[i], "")
		require.NoError(t, err)
		// [3] triggerTimeStamp, 9 octets, on the wire.
		require.True(t, bytes.Contains(encoded, append([]byte{0x83, 0x09}, w...)), "container %d: % x", i, encoded)
		var decoded cdrType.UsedUnitContainer
		require.NoError(t, asn.UnmarshalWithParams(encoded, &decoded, ""))
		require.NotNil(t, decoded.TriggerTimeStamp, "container %d", i)
		require.Equal(t, w, decoded.TriggerTimeStamp.Value, "container %d", i)
	}
}
