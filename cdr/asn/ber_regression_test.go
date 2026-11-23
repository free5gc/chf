package asn

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// boundaryChoice mirrors the CDR CHOICE shape: an explicitly tagged CHOICE
// field whose alternatives carry their own context-specific tags.
type boundaryChoice struct {
	Present int
	O       *OctetString `ber:"tagNum:0"`
	S       *boundarySeq `ber:"tagNum:1"`
}

type boundarySeq struct {
	Data OctetString `ber:"tagNum:0"`
}

// TestUnmarshalChoiceHeaderBoundaries round-trips a tagged CHOICE whose
// content length sits on the BER length-encoding boundaries, where the outer
// (choice) header and the inner (alternative) header have different sizes.
// The embedded-choice branch of ParseField advanced by the inner header length
// instead of the outer one, so the alternative was parsed one byte shifted
// whenever the header sizes differed: corrupt data or a decode error.
func TestUnmarshalChoiceHeaderBoundaries(t *testing.T) {
	t.Parallel()

	// Inner content lengths around the 1-octet (127), 2-octet (255) and
	// 3-octet (65535) length forms. At 125/128/250/256/65531/65536 the outer
	// and inner headers have the same size; at the others they differ.
	sizes := []int{125, 126, 127, 128, 250, 253, 254, 255, 256, 65531, 65532, 65535, 65536}

	for _, n := range sizes {
		t.Run(fmt.Sprintf("octetstring-%d", n), func(t *testing.T) {
			t.Parallel()

			data := OctetString(bytes.Repeat([]byte{0xAB}, n))
			in := boundaryChoice{Present: 1, O: &data}

			enc, err := BerMarshalWithParams(in, "tagNum:5,choice")
			require.NoError(t, err)

			var out boundaryChoice
			err = UnmarshalWithParams(enc, &out, "tagNum:5,choice")
			require.NoError(t, err)
			require.Equal(t, 1, out.Present)
			require.NotNil(t, out.O)
			require.Equal(t, []byte(data), []byte(*out.O),
				"CHOICE content of %d bytes must round-trip unshifted", n)
		})
	}

	// SEQUENCE alternative at the 126-byte boundary: the shifted parse does
	// not yield a value here; the decode fails.
	t.Run("sequence-126", func(t *testing.T) {
		t.Parallel()

		// The element inside the SEQUENCE is a 2-byte header plus 124 bytes of
		// data, so the SEQUENCE alternative's content is exactly 126 bytes.
		in := boundaryChoice{
			Present: 2,
			S:       &boundarySeq{Data: OctetString(bytes.Repeat([]byte{0xCD}, 124))},
		}

		enc, err := BerMarshalWithParams(in, "tagNum:5,choice")
		require.NoError(t, err)

		var out boundaryChoice
		err = UnmarshalWithParams(enc, &out, "tagNum:5,choice")
		require.NoError(t, err)
		require.Equal(t, 2, out.Present)
		require.NotNil(t, out.S)
		require.True(t, reflect.DeepEqual(*in.S, *out.S))
	})
}

// untaggedFieldsSeq mirrors cdrType.IPBinV6AddressWithPrefixLength: a SEQUENCE
// whose fields carry no tagNum, so they are encoded with their universal tags.
type untaggedFieldsSeq struct {
	Addr   OctetString
	Prefix *int `ber:"optional,default:64"`
}

// TestUnmarshalUntaggedStructFields round-trips a SEQUENCE whose fields have
// no tagNum in their ber tag. The struct-decoding loops dereferenced
// structParams[i].tagNumber unconditionally, so such fields (e.g.
// IPBinV6AddressWithPrefixLength) panicked the decoder.
func TestUnmarshalUntaggedStructFields(t *testing.T) {
	t.Parallel()

	prefix := 48
	cases := []struct {
		name string
		in   untaggedFieldsSeq
	}{
		{"both fields", untaggedFieldsSeq{Addr: OctetString{1, 2, 3, 4}, Prefix: &prefix}},
		{"optional absent", untaggedFieldsSeq{Addr: OctetString{5, 6}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			enc, err := BerMarshalWithParams(tc.in, "seq")
			require.NoError(t, err)

			var out untaggedFieldsSeq
			require.NotPanics(t, func() {
				err = UnmarshalWithParams(enc, &out, "seq")
			})
			require.NoError(t, err)
			require.True(t, reflect.DeepEqual(tc.in, out))
		})
	}
}

type choiceSliceStruct struct {
	A []choiceTest `ber:"tagNum:0"`
}

// TestUnmarshalSliceOfChoice round-trips a slice of CHOICE elements held in a
// tagged struct field (the shape of ChargingRecord.Triggers). The encoder
// clears the tag parameter for slice elements (the context-specific tag
// belongs to the slice, not its elements), but the decoder passed the slice's
// params through to each element, so CHOICE elements were parsed as embedded
// (tagged) choices and never decoded.
func TestUnmarshalSliceOfChoice(t *testing.T) {
	t.Parallel()

	in := choiceSliceStruct{
		A: []choiceTest{
			{1, &i, nil, nil, nil, nil},
			{3, nil, nil, &intStruct{64}, nil, nil},
		},
	}

	enc, err := BerMarshalWithParams(in, "seq")
	require.NoError(t, err)

	var out choiceSliceStruct
	err = UnmarshalWithParams(enc, &out, "seq")
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(in, out))
}
