package charging

import (
	"fmt"

	"github.com/free5gc/chf/cdr/cdrType"
)

type CDRState struct {
	Subscriber string
	NotifyURI  string
	Current    *cdrType.CHFRecord
	Closed     bool
}

// CDRProgress prevents a local retry from appending usage to the CDR twice.
type CDRProgress struct {
	EventID string
	Closed  bool
}

func CDRIdentity(record *cdrType.CHFRecord) (string, error) {
	if record == nil || record.ChargingFunctionRecord == nil ||
		record.ChargingFunctionRecord.ChargingSessionIdentifier == nil {
		return "", fmt.Errorf("CDR has no charging resource identity")
	}
	cdr := record.ChargingFunctionRecord
	sequence := int64(0)
	if cdr.RecordSequenceNumber != nil {
		sequence = *cdr.RecordSequenceNumber
	}
	return Digest([]any{string(cdr.ChargingSessionIdentifier.Value), sequence})
}
