package asn

import (
	"fmt"
	"reflect"
)

// parse and return tag and length, also the length of two parts
func parseTagAndLength(bytes []byte) (r tagAndLen, off int, e error) {
	off++
	r.class = int(bytes[0] >> 6)
	r.constructed = (bytes[0] & 0x20) != 0
	if bytes[0]&0x1f != 0x1f {
		r.tagNumber = uint64(bytes[0] & 0x1f)
	} else {
		for off < len(bytes) {
			r.tagNumber <<= 7
			r.tagNumber |= uint64(bytes[off] & 0x7f)
			off++
			if bytes[off-1]&0x80 == 0 {
				break
			}
		}
		if off > 10 {
			e = fmt.Errorf("tag number is too large")
			return
		}
	}

	if off >= len(bytes) {
		// fmt.Println(bytes)
		e = fmt.Errorf("panic")
		return
	}
	if bytes[off] <= 127 {
		r.len = int64(bytes[off])
		off++
	} else {
		len := int(bytes[off] & 0x7f)
		// fmt.Println("len", len)
		if len > 3 {
			e = fmt.Errorf("length is too large")
		}
		off++
		var val int64
		val, e = parseInt64(bytes[off : off+len])
		if e != nil {
			return
		}
		// fmt.Println("bytes[off : off+len]", bytes[off : off+len], "val", val)

		r.len = int64(val)
		off += len
	}

	return
}

func parseBitString(bytes []byte) (r BitString, e error) {
	r.BitLength = uint64((len(bytes)-1)*8 - int(bytes[0]))
	r.Bytes = bytes[1:]
	return
}

func parseInt64(bytes []byte) (r int64, e error) {
	if len(bytes) > 8 {
		e = fmt.Errorf("out of range of int64")
		return
	}

	minus := false
	if uint(bytes[0]) > 127 {
		minus = true
	}

	for i := 0; i < 8-len(bytes); i++ {
		extend_byte := byte(0)
		if minus {
			extend_byte = byte(255)
		}
		bytes = append([]byte{extend_byte}, bytes...)
	}

	if minus {
		for i := range bytes {
			bytes[i] = ^bytes[i]
		}
	}

	for _, b := range bytes {
		r <<= 8
		r |= int64(b)
	}

	if minus {
		r = -r - 1
	}

	return
}

func parseBool(b byte) (bool, error) {
	return b != 0, nil
}

// ParseField is the main parsing function. Given a byte slice containing type value,
// it will try to parse a suitable ASN.1 value out and store it
// in the given Value. TODO : ObjectIdenfier
func ParseField(v reflect.Value, bytes []byte, params fieldParameters) error {
	fieldType := v.Type()

	// If we have run out of data return error.
	if v.Kind() == reflect.Ptr {
		ptr := reflect.New(fieldType.Elem())
		v.Set(ptr)
		return ParseField(v.Elem(), bytes, params)
	}

	tal, talOff, err := parseTagAndLength(bytes)
	if err != nil {
		return err
	}
	if int64(talOff)+tal.len > int64(len(bytes)) {
		return fmt.Errorf("type value out of range")
	}

	// We deal with the structures defined in this package first.
	switch fieldType {
	case BitStringType:
		val, err := parseBitString(bytes[talOff:])
		if err != nil {
			return err
		}

		v.Set(reflect.ValueOf(val))
		return nil
	case ObjectIdentifierType:
		return fmt.Errorf("Unsupport ObjectIdenfier type")
	case OctetStringType:
		val := bytes[talOff:]
		v.Set(reflect.ValueOf(val))
		return nil
	case EnumeratedType:
		val, err := parseInt64(bytes[talOff:])
		if err != nil {
			return err
		}

		v.Set(reflect.ValueOf(Enumerated(val)))
		return nil
	case NullType:
		val := true
		v.Set(reflect.ValueOf(val))
		return nil
	}
	switch val := v; val.Kind() {
	case reflect.Bool:
		if parsedBool, err := parseBool(bytes[talOff]); err != nil {
			return err
		} else {
			val.SetBool(parsedBool)
			return nil
		}
	case reflect.Int, reflect.Int32, reflect.Int64:
		if parsedInt, err := parseInt64(bytes[talOff:]); err != nil {
			return err
		} else {
			val.SetInt(parsedInt)
			return nil
		}
	case reflect.Struct:

		structType := fieldType
		var structParams []fieldParameters

		if structType.Field(0).Name == "Value" {
			// Non struct type
			// fmt.Println("Non struct type")
			return ParseField(val.Field(0), bytes, params)
		} else if structType.Field(0).Name == "List" {
			// List Type: SEQUENCE/SET OF
			// fmt.Println("List type")
			return ParseField(val.Field(0), bytes, params)
		}

		// parse parameters
		for i := 0; i < structType.NumField(); i++ {
			if structType.Field(i).PkgPath != "" {
				return fmt.Errorf("struct contains unexported fields : " + structType.Field(i).PkgPath)
			}
			tempParams := parseFieldParameters(structType.Field(i).Tag.Get("ber"))
			structParams = append(structParams, tempParams)
		}

		// CHOICE or OpenType
		if structType.NumField() > 0 && structType.Field(0).Name == "Present" {
			var present int = 0

			if params.openType {
				return fmt.Errorf("OpenType is not implemented")
			} else {
				offset := 0
				// embed choice type
				if params.tagNumber != nil {
					tal, talOff, err = parseTagAndLength(bytes[talOff:])
					if err != nil {
						return err
					}
					if int64(talOff)+tal.len > int64(len(bytes)) {
						return fmt.Errorf("type value out of range")
					}
					offset += talOff
				}

				for i := 1; i < structType.NumField(); i++ {
					if structParams[i].tagNumber == nil {
						// TODO: choice type with a universal tag
					} else if *structParams[i].tagNumber == tal.tagNumber {
						present = i
						break
					}
				}
				val.Field(0).SetInt(int64(present))
				if present == 0 {
					return fmt.Errorf("CHOICE present is 0(present's field number)")
				} else if present >= structType.NumField() {
					return fmt.Errorf("CHOICE Present is bigger than number of struct field")
				} else {
					return ParseField(val.Field(present), bytes[offset:], structParams[present])
				}
			}
		}

		offset := int64(talOff)
		totalLen := int64(len(bytes))

		if !params.set {
			current := 0
			next := int64(0)
			for ; offset < totalLen; offset = next {
				tal, talOff, err := parseTagAndLength(bytes[offset:])
				if err != nil {
					return err
				}
				next = int64(offset) + int64(talOff) + tal.len
				if next > totalLen {
					return fmt.Errorf("type value out of range")
				}
				if offset >= next {
					fmt.Println("bytes offset", offset, "next", next, "talOff", talOff, "tal.len", tal.len)
				}

				for ; current < structType.NumField(); current++ {
					// for open type reference
					if params.openType {
						return fmt.Errorf("OpenType is not implemented")
					}
					if *structParams[current].tagNumber == tal.tagNumber {
						if err := ParseField(val.Field(current), bytes[offset:next], structParams[current]); err != nil {
							return err
						}
						break
					}
				}
				if current >= structType.NumField() {
					return fmt.Errorf("corresponding type not found")
				}
				current++
			}
		} else {
			next := int64(0)
			for ; offset < totalLen; offset = next {
				tal, talOff, err := parseTagAndLength(bytes[offset:])
				if err != nil {
					return err
				}
				next = offset + int64(talOff) + tal.len
				if next > totalLen {
					return fmt.Errorf("type value out of range")
				}

				current := 0
				for ; current < structType.NumField(); current++ {
					// for open type reference
					if params.openType {
						return fmt.Errorf("OpenType is not implemented")
					}
					if *structParams[current].tagNumber == tal.tagNumber {
						if err := ParseField(val.Field(current), bytes[offset:next], structParams[current]); err != nil {
							return err
						}
						break
					}
				}
				if current >= structType.NumField() {
					return fmt.Errorf("corresponding type not found")
				}
			}
		}
		return nil
	case reflect.Slice:
		sliceType := fieldType
		var valArray [][]byte
		var next int64
		for offset := int64(talOff); offset < int64(len(bytes)); offset = next {
			tal, talOff, err := parseTagAndLength(bytes[offset:])
			if err != nil {
				return err
			}
			next = offset + int64(talOff) + tal.len
			if next > int64(len(bytes)) {
				return fmt.Errorf("type value out of range")
			}
			valArray = append(valArray, bytes[offset:next])
		}

		sliceLen := len(valArray)
		newSlice := reflect.MakeSlice(sliceType, sliceLen, sliceLen)
		for i := 0; i < sliceLen; i++ {
			err := ParseField(newSlice.Index(i), valArray[i], params)
			if err != nil {
				return err
			}
		}

		val.Set(newSlice)
		return nil
	case reflect.String:
		val.SetString(string(bytes[talOff:]))
		return nil
	}

	return fmt.Errorf("unsupported: " + v.Type().String())
}

// Unmarshal parses the BER-encoded ASN.1 data structure b
// and uses the reflect package to fill in an arbitrary value pointed at by value.
// Because Unmarshal uses the reflect package, the structs
// being written to must use upper case field names.
//
// An ASN.1 INTEGER can be written to an int, int32, int64,
// If the encoded value does not fit in the Go type,
// Unmarshal returns a parse error.
//
// An ASN.1 BIT STRING can be written to a BitString.
//
// An ASN.1 OCTET STRING can be written to a []byte.
//
// An ASN.1 OBJECT IDENTIFIER can be written to an
// ObjectIdentifier.
//
// An ASN.1 ENUMERATED can be written to an Enumerated.
//
// Any of the above ASN.1 values can be written to an interface{}.
// The value stored in the interface has the corresponding Go type.
// For integers, that type is int64.
//
// An ASN.1 SEQUENCE OF x can be written
// to a slice if an x can be written to the slice's element type.
//
// An ASN.1 SEQUENCE can be written to a struct
// if each of the elements in the sequence can be
// written to the corresponding element in the struct.
//
// The following tags on struct fields have special meaning to Unmarshal:
//
//	optional        	OPTIONAL tag in SEQUENCE
//	sizeLB		        set the minimum value of size constraint
//	sizeUB              set the maximum value of value constraint
//	valueLB		        set the minimum value of size constraint
//	valueUB             set the maximum value of value constraint
//	default             sets the default value
//	openType            specifies the open Type
//  referenceFieldName	the string of the reference field for this type (only if openType used)
//  referenceFieldValue	the corresponding value of the reference field for this type (only if openType used)
//
// Other ASN.1 types are not supported; if it encounters them,
// Unmarshal returns a parse error.
func Unmarshal(b []byte, value interface{}) error {
	return UnmarshalWithParams(b, value, "")
}

// UnmarshalWithParams allows field parameters to be specified for the
// top-level element. The form of the params is the same as the field tags.
func UnmarshalWithParams(b []byte, value interface{}, params string) error {
	v := reflect.ValueOf(value).Elem()
	return ParseField(v, b, parseFieldParameters(params))
}
