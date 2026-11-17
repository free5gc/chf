package asn

import (
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUnmarshalRejectsIndefiniteLength feeds elements using the BER
// indefinite-length form (length octet 0x80, value terminated by 00 00).
// The CDR encoder only emits definite lengths; an indefinite length used to
// parse as length 0, so the element was misread.
func TestUnmarshalRejectsIndefiniteLength(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		out   interface{}
		in    string
		param string
	}{
		{"octet string", newOctetString(nil), "0480" + "0000", ""},
		{"sequence", &intStruct{}, "3080" + "80012a" + "0000", "seq"},
		{"integer", newInt(0), "0280" + "0000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := hex.DecodeString(tc.in)
			require.NoError(t, err)
			out := reflect.New(reflect.TypeOf(tc.out).Elem())
			err = UnmarshalWithParams(in, out.Interface(), tc.param)
			require.Error(t, err)
			require.Contains(t, err.Error(), "indefinite")
		})
	}
}

// TestUnmarshalRejectsEmptyInteger: a BER INTEGER has at least one content
// octet (X.690 8.3.1); zero-length content used to decode silently as 0.
func TestUnmarshalRejectsEmptyInteger(t *testing.T) {
	t.Parallel()

	in, err := hex.DecodeString("0200")
	require.NoError(t, err)
	var got int
	err = Unmarshal(in, &got)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

// TestParseBitStringInvalidUnusedBits: the initial octet of a BIT STRING
// counts unused bits in the final octet and must be 0..7 (X.690 8.6.2.2), and
// 0 when there are no content octets (8.6.2.3). Larger values gave a wrong
// BitLength ((len-1)*8 - unused), or underflowed it into a huge uint64.
func TestParseBitStringInvalidUnusedBits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string // full TLV
	}{
		{"unused bits > 7", "0302" + "08aa"},
		{"unused bits without content", "0301" + "04"},
		{"unused bits huge", "0302" + "ffaa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := hex.DecodeString(tc.in)
			require.NoError(t, err)
			var bs BitString
			err = Unmarshal(in, &bs)
			require.Error(t, err)
			require.Contains(t, err.Error(), "unused bits")
		})
	}
}

// TestUnmarshalNullField: decoding into a NULL-typed field used to panic in
// reflect ("value of type bool is not assignable to type asn.NULL"). CDR types
// carry optional NULL fields (SUPIunauthenticatedFlag).
func TestUnmarshalNullField(t *testing.T) {
	t.Parallel()

	type nullStruct struct {
		Flag *NULL `ber:"tagNum:3,optional"`
	}

	// SEQUENCE { [3] NULL }
	in, err := hex.DecodeString("3002" + "8300")
	require.NoError(t, err)

	var got nullStruct
	require.NoError(t, UnmarshalWithParams(in, &got, "seq"))
	require.NotNil(t, got.Flag)
	require.Equal(t, NULL(true), *got.Flag)
}

// TestBitStringAlignedRoundTrip: an octet-aligned BIT STRING (BitLength%8==0)
// must be encoded with unused-bits octet 0, not 8 (X.690 8.6.2.2), and an
// empty one as the single octet 0 (8.6.2.3).
func TestBitStringAlignedRoundTrip(t *testing.T) {
	t.Parallel()

	data, err := BerMarshal(BitString{Bytes: []byte{0xaa}, BitLength: 8})
	require.NoError(t, err)
	require.Equal(t, "030200aa", hex.EncodeToString(data))

	var got BitString
	require.NoError(t, Unmarshal(data, &got))
	require.Equal(t, BitString{Bytes: []byte{0xaa}, BitLength: 8}, got)

	data, err = BerMarshal(BitString{})
	require.NoError(t, err)
	require.Equal(t, "030100", hex.EncodeToString(data))
}

// TestParseTagAndLengthMalformed: truncated headers return an error instead
// of panicking on out-of-range indexing.
func TestParseTagAndLengthMalformed(t *testing.T) {
	t.Parallel()

	for name, in := range map[string][]byte{
		"empty":                  {},
		"tag-only":               {0x04},       // tag, no length octet
		"longform-len-truncated": {0x30, 0x82}, // "2 length octets follow", none present
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				_, _, err = parseTagAndLength(in)
			})
			require.Error(t, err)
		})
	}
}

// TestParseFieldEmptyValueGuards feeds zero-length TLVs to the BIT STRING,
// ENUMERATED and BOOLEAN branches of ParseField; they return an error instead
// of panicking on out-of-range indexing.
func TestParseFieldEmptyValueGuards(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		typ    reflect.Type
		data   []byte // TLV with an empty value
		errSub string
	}{
		{"bit string", BitStringType, []byte{0x03, 0x00}, "bit string"},
		{"enumerated", EnumeratedType, []byte{0x0a, 0x00}, "enumerated"},
		{"bool", reflect.TypeOf(false), []byte{0x01, 0x00}, "bool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := reflect.New(tc.typ).Elem()
			var err error
			require.NotPanics(t, func() {
				err = ParseField(v, tc.data, fieldParameters{})
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errSub)
		})
	}
}
