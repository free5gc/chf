package cdrType

// Need to import "gofree5gc/lib/aper" if it uses "aper"

const (
	CHFRecordPresentNothing int = iota /* No components present */
	CHFRecordPresentChargingFunctionRecord
)

type CHFRecord struct {
	Present                int             /* Choice Type */
	ChargingFunctionRecord *ChargingRecord `ber:"tagNum:200"`
}
