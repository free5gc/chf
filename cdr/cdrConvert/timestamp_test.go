package cdrConvert

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/chf/cdr/asn"
)

// TS 32.298 TimeStamp: YYMMDDhhmmssShhmm, local time then the offset from
// UTC, BCD except the sign.
func TestTimeStampToCdrZoneOffset(t *testing.T) {
	tests := []struct {
		zone   string
		offset int
		want   asn.OctetString
	}{
		{"UTC", 0, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x00, 0x00}},
		{"+01:00", 3600, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x01, 0x00}},
		{"-05:00", -5 * 3600, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '-', 0x05, 0x00}},
		{"+05:30", 5*3600 + 30*60, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x05, 0x30}},
		{"-03:30", -(3*3600 + 30*60), asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '-', 0x03, 0x30}},
		{"+05:45", 5*3600 + 45*60, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x05, 0x45}},
		{"+14:00", 14 * 3600, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '+', 0x14, 0x00}},
		{"-12:00", -12 * 3600, asn.OctetString{0x26, 0x11, 0x04, 0x23, 0x59, 0x30, '-', 0x12, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			local := time.Date(2026, 11, 4, 23, 59, 30, 0, time.FixedZone(tt.zone, tt.offset))
			require.Equal(t, tt.want, TimeStampToCdr(&local).Value)
		})
	}
}
