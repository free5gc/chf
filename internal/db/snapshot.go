package db

import (
	"bytes"
	"reflect"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsoncodec"
	"go.mongodb.org/mongo-driver/bson/bsonrw"
)

// Diameter Time is a distinct Go type without a BSON codec. Preserve every
// timestamp (including zero values and nanoseconds) in settlement snapshots.
var snapshotRegistry = newSnapshotRegistry()

func newSnapshotRegistry() *bsoncodec.Registry {
	registry := bson.NewRegistry()
	registry.RegisterTypeEncoder(reflect.TypeOf(datatype.Time{}), bsoncodec.ValueEncoderFunc(
		func(_ bsoncodec.EncodeContext, writer bsonrw.ValueWriter, value reflect.Value) error {
			stamp := time.Time(value.Interface().(datatype.Time))
			return writer.WriteString(stamp.Format(time.RFC3339Nano))
		}))
	registry.RegisterTypeDecoder(reflect.TypeOf(datatype.Time{}), bsoncodec.ValueDecoderFunc(
		func(_ bsoncodec.DecodeContext, reader bsonrw.ValueReader, value reflect.Value) error {
			encoded, err := reader.ReadString()
			if err != nil {
				return err
			}
			stamp, err := time.Parse(time.RFC3339Nano, encoded)
			if err != nil {
				return err
			}
			value.Set(reflect.ValueOf(datatype.Time(stamp)))
			return nil
		}))
	return registry
}

func EncodeSnapshot(value any) ([]byte, error) {
	var buffer bytes.Buffer
	writer, err := bsonrw.NewBSONValueWriter(&buffer)
	if err != nil {
		return nil, err
	}
	encoder, err := bson.NewEncoder(writer)
	if err != nil {
		return nil, err
	}
	if err = encoder.SetRegistry(snapshotRegistry); err != nil {
		return nil, err
	}
	if err = encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func DecodeSnapshot(data []byte, value any) error {
	decoder, err := bson.NewDecoder(bsonrw.NewBSONDocumentReader(data))
	if err != nil {
		return err
	}
	if err = decoder.SetRegistry(snapshotRegistry); err != nil {
		return err
	}
	return decoder.Decode(value)
}
