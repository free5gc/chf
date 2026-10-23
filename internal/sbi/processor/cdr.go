package processor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/free5gc/chf/cdr/asn"
	"github.com/free5gc/chf/cdr/cdrConvert"
	"github.com/free5gc/chf/cdr/cdrFile"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
)

func buildCdrFileName(ueid string) string {
	sum := sha256.Sum256([]byte(ueid))
	return "/tmp/chf-" + hex.EncodeToString(sum[:]) + ".cdr"
}

func (p *Processor) OpenCDR(
	chargingData models.Chf_ConvCharging_ChargingDataRequest,
	ue *chf_context.ChfUe,
	sessionId string,
	partialRecord bool,
) (*cdrType.CHFRecord, error) {
	// 32.298 5.1.5.0.1 for CHF CDR field
	var chfCdr cdrType.ChargingRecord
	logger.ChargingdataPostLog.Tracef("Open CDR")

	self := chf_context.GetSelf()

	// Record Sequence Number(Conditional IE): Partial record sequence number, only present in case of partial records.
	// Partial CDR: Fragments of CDR, for long session charging
	if partialRecord {
		cdr, ok := ue.Cdr[sessionId]
		if !ok || cdr == nil {
			return nil, fmt.Errorf("charging session[%s] not found", sessionId)
		}
		if cdr.ChargingFunctionRecord == nil {
			return nil, fmt.Errorf("charging session[%s] has invalid CDR", sessionId)
		}

		partialRecordSeqNum := self.RecordSequenceNumber[sessionId]
		partialRecordSeqNum++
		cdr.ChargingFunctionRecord.RecordSequenceNumber = &(partialRecordSeqNum)

		return cdr, nil
	}

	chfCdr.RecordType = cdrType.RecordType{
		Value: 200,
	}

	// TODO IA5 string coversion
	chfCdr.RecordingNetworkFunctionID = cdrType.NetworkFunctionName{
		Value: asn.IA5String(self.NfId),
	}

	// RecordOpeningTime:
	//  Time stamp when the PDU session is activated in the SMF or record opening time on subsequent partial records.
	// TODO:
	//  identify charging event is SMF PDU session
	t := time.Now()
	chfCdr.RecordOpeningTime = cdrConvert.TimeStampToCdr(&t)

	// Initial CDR duration
	chfCdr.Duration = cdrType.CallDuration{
		Value: 0,
	}

	// 32.298 5.1.5.1.5 Local Record Sequence Number
	// TODO determine local record sequnece number
	self.Lock()
	self.LocalRecordSequenceNumber++
	chfCdr.LocalRecordSequenceNumber = &cdrType.LocalSequenceNumber{
		Value: int64(self.LocalRecordSequenceNumber),
	}
	self.Unlock()
	// Skip Record Extensions: operator/manufacturer specific extensions

	supiType := strings.Split(ue.Supi, "-")[0]
	switch supiType {
	case "imsi":
		chfCdr.SubscriberIdentifier = &cdrType.SubscriptionID{
			SubscriptionIDType: cdrType.SubscriptionIDType{Value: cdrType.SubscriptionIDTypePresentENDUSERIMSI},
			SubscriptionIDData: asn.UTF8String(ue.Supi[5:]),
		}
	case "nai":
		chfCdr.SubscriberIdentifier = &cdrType.SubscriptionID{
			SubscriptionIDType: cdrType.SubscriptionIDType{Value: cdrType.SubscriptionIDTypePresentENDUSERNAI},
			SubscriptionIDData: asn.UTF8String(ue.Supi[4:]),
		}
	case "gci":
		chfCdr.SubscriberIdentifier = &cdrType.SubscriptionID{
			SubscriptionIDType: cdrType.SubscriptionIDType{Value: cdrType.SubscriptionIDTypePresentENDUSERNAI},
			SubscriptionIDData: asn.UTF8String(ue.Supi[4:]),
		}
	case "gli":
		chfCdr.SubscriberIdentifier = &cdrType.SubscriptionID{
			SubscriptionIDType: cdrType.SubscriptionIDType{Value: cdrType.SubscriptionIDTypePresentENDUSERNAI},
			SubscriptionIDData: asn.UTF8String(ue.Supi[4:]),
		}
	}

	if sessionId != "" {
		chfCdr.ChargingSessionIdentifier = &cdrType.ChargingSessionIdentifier{
			Value: asn.OctetString(sessionId),
		}
	}

	chfCdr.ChargingID = &cdrType.ChargingID{
		Value: int64(chargingData.ChargingId),
	}

	if chargingData.NfConsumerIdentification == nil {
		logger.ChargingdataPostLog.Warnln("nFConsumerIdentification is not presented")
		return nil, fmt.Errorf("nFConsumerIdentification is not presented")
	}
	consumerIdentity := chargingData.NfConsumerIdentification

	var consumerInfo cdrType.NetworkFunctionInformation
	if consumerName := consumerIdentity.NFName; consumerName != "" {
		consumerInfo.NetworkFunctionName = &cdrType.NetworkFunctionName{
			Value: asn.IA5String(consumerIdentity.NFName),
		}
	}
	if consumerV4Addr := consumerIdentity.NFIPv4Address; consumerV4Addr != "" {
		consumerInfo.NetworkFunctionIPv4Address = &cdrType.IPAddress{
			Present:         3,
			IPTextV4Address: (*asn.IA5String)(&consumerV4Addr),
		}
	}
	if consumerV6Addr := consumerIdentity.NFIPv6Address; consumerV6Addr != "" {
		consumerInfo.NetworkFunctionIPv6Address = &cdrType.IPAddress{
			Present:         4,
			IPTextV6Address: (*asn.IA5String)(&consumerV6Addr),
		}
	}
	if consumerFqdn := consumerIdentity.NFFqdn; consumerFqdn != "" {
		consumerInfo.NetworkFunctionFQDN = &cdrType.NodeAddress{
			Present:    2,
			DomainName: (*asn.GraphicString)(&consumerFqdn),
		}
	}
	if consumerPlmnId := chargingData.NfConsumerIdentification.NFPLMNID; consumerPlmnId != nil {
		plmnIdByte, err := cdrConvert.PlmnIdToCdr(*consumerPlmnId)
		if err != nil {
			logger.ChargingdataPostLog.Warnf("invalid nFPLMNID: %v", err)
			return nil, err
		}
		consumerInfo.NetworkFunctionPLMNIdentifier = &cdrType.PLMNId{
			Value: plmnIdByte.Value,
		}
	}
	logger.ChargingdataPostLog.Infof("%s charging event", consumerIdentity.NodeFunctionality)
	switch consumerIdentity.NodeFunctionality {
	case "SMF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentSMF
	case "AMF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentAMF
	case "SMSF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentSMSF
	case "PGW_C_SMF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentPGWCSMF
	case "NEF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentNEF
	case "SGW":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentSGW
	case "I_SMF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentISMF
	case "ePDG":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentEPDG
	case "CEF":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentCEF
	case "MnS_Producer":
		consumerInfo.NetworkFunctionality.Value = cdrType.NetworkFunctionalityPresentMnSProducer
	}
	chfCdr.NFunctionConsumerInformation = consumerInfo

	if serviceSpecInfo := asn.OctetString(chargingData.ServiceSpecificationInfo); len(serviceSpecInfo) != 0 {
		chfCdr.ServiceSpecificationInformation = &serviceSpecInfo
	}

	// TODO: encode service specific data to CDR
	if registerInfo := chargingData.RegistrationChargingInformation; registerInfo != nil {
		logger.ChargingdataPostLog.Debugln("Registration Charging Event")
		chfCdr.RegistrationChargingInformation = &cdrType.RegistrationChargingInformation{
			RegistrationMessagetype: cdrType.RegistrationMessageType{Value: cdrType.RegistrationMessageTypePresentInitial},
		}
	}
	if pduSessionInfo := chargingData.PDUSessionChargingInformation; pduSessionInfo != nil {
		logger.ChargingdataPostLog.Debugln("PDU Session Charging Event")
		pduInfo := pduSessionInfo.PduSessionInformation
		if pduInfo == nil {
			logger.ChargingdataPostLog.Warnln("PDU Session Information is nil")
			return nil, fmt.Errorf("PDU Session Information is not presented")
		}

		if pduInfo.PduSessionID == 0 {
			logger.ChargingdataPostLog.Warnln("PDU Session ID is nil")
			return nil, fmt.Errorf("PDU Session ID is not presented")
		}

		if pduInfo.DnnId == "" {
			logger.ChargingdataPostLog.Warnln("DNN ID is nil")
			return nil, fmt.Errorf("DNN ID is not presented")
		}

		if pduInfo.NetworkSlicingInfo == nil {
			logger.ChargingdataPostLog.Warnln("Network Slicing Info is nil")
			return nil, fmt.Errorf("network Slicing Info is not presented")
		}

		if pduInfo.NetworkSlicingInfo.SNSSAI == nil {
			logger.ChargingdataPostLog.Warnln("SNSSAI is nil")
			return nil, fmt.Errorf("SNSSAI is not presented")
		}

		chfCdr.PDUSessionChargingInformation = &cdrType.PDUSessionChargingInformation{
			PDUSessionChargingID: cdrType.ChargingID{
				Value: int64(pduSessionInfo.ChargingId),
			},
			PDUSessionId: cdrType.PDUSessionId{
				Value: int64(pduInfo.PduSessionID),
			},
			NetworkSliceInstanceID: &cdrType.SingleNSSAI{
				SST: cdrType.SliceServiceType{
					Value: int64(pduInfo.NetworkSlicingInfo.SNSSAI.Sst),
				},
				SD: &cdrType.SliceDifferentiator{
					Value: []byte(pduInfo.NetworkSlicingInfo.SNSSAI.Sd),
				},
			},
			DataNetworkNameIdentifier: &cdrType.DataNetworkNameIdentifier{
				Value: asn.IA5String(pduInfo.DnnId),
			},
		}
	}

	chfCdr.ChargingID.Value = int64(chargingData.ChargingId)

	cdr := cdrType.CHFRecord{
		Present:                1,
		ChargingFunctionRecord: &chfCdr,
	}

	return &cdr, nil
}

func (p *Processor) UpdateCDR(
	record *cdrType.CHFRecord, chargingData models.Chf_ConvCharging_ChargingDataRequest,
) error {
	if record == nil || record.ChargingFunctionRecord == nil {
		return fmt.Errorf("CHFRecord is nil")
	}

	// map SBI IE to CDR field
	chfCdr := record.ChargingFunctionRecord

	if len(chargingData.MultipleUnitUsage) != 0 {
		// NOTE: quota info needn't be encoded to cdr, refer 32.291 Ch7.1
		cdrMultiUnitUsage := cdrConvert.MultiUnitUsageToCdr(chargingData.MultipleUnitUsage)
		chfCdr.ListOfMultipleUnitUsage = append(chfCdr.ListOfMultipleUnitUsage, cdrMultiUnitUsage...)
	}

	if len(chargingData.Triggers) != 0 {
		triggers := cdrConvert.TriggersToCdr(chargingData.Triggers)
		chfCdr.Triggers = append(chfCdr.Triggers, triggers...)
	}

	return nil
}

func (p *Processor) CloseCDR(record *cdrType.CHFRecord, partial bool) error {
	logger.ChargingdataPostLog.Infof("Close CDR")

	chfCdr := record.ChargingFunctionRecord

	// Initial Cause for record closing
	// 	normalRelease  (0),
	// partialRecord  (1),
	// abnormalRelease  (4),
	// cAMELInitCallRelease  (5),
	// volumeLimit	 (16),
	// timeLimit	 (17),
	// servingNodeChange	 (18),
	// maxChangeCond	 (19),
	// managementIntervention	 (20),
	// intraSGSNIntersystemChange	 (21),
	// rATChange	 (22),
	// mSTimeZoneChange	 (23),
	// sGSNPLMNIDChange	 (24),
	// sGWChange	 (25),
	// aPNAMBRChange	 (26),
	// mOExceptionDataCounterReceipt	 (27),
	// unauthorizedRequestingNetwork	 (52),
	// unauthorizedLCSClient	 (53),
	// positionMethodFailure	 (54),
	// unknownOrUnreachableLCSClient	 (58),
	// listofDownstreamNodeChange	 (59)
	if partial {
		if chfCdr.RecordSequenceNumber == nil {
			sequence := int64(0)
			chfCdr.RecordSequenceNumber = &sequence
		}
		chfCdr.CauseForRecClosing = cdrType.CauseForRecClosing{Value: 1}
	} else {
		chfCdr.CauseForRecClosing = cdrType.CauseForRecClosing{Value: 0}
	}

	return nil
}

func dumpCdrFile(ueid string, records []*cdrType.CHFRecord) error {
	var cdrfile cdrFile.CDRFile
	cdrfile.Hdr.LengthOfCdrRouteingFilter = 0
	cdrfile.Hdr.LengthOfPrivateExtension = 0
	cdrfile.Hdr.HeaderLength = uint32(52 + cdrfile.Hdr.LengthOfCdrRouteingFilter + cdrfile.Hdr.LengthOfPrivateExtension)
	if cdrfile.Hdr.HighReleaseIdentifier == 7 {
		cdrfile.Hdr.HeaderLength++
	}
	if cdrfile.Hdr.LowReleaseIdentifier == 7 {
		cdrfile.Hdr.HeaderLength++
	}
	cdrfile.Hdr.NumberOfCdrsInFile = uint32(len(records))
	cdrfile.Hdr.FileLength = cdrfile.Hdr.HeaderLength
	logger.ChargingdataPostLog.Traceln("cdrfile.Hdr.NumberOfCdrsInFile:", uint32(len(records)))

	for _, record := range records {
		cdrBytes, err := asn.BerMarshalWithParams(&record, "explicit,choice")
		if err != nil {
			return fmt.Errorf("encode CDR: %w", err)
		}

		if len(cdrBytes) > math.MaxUint16 {
			return fmt.Errorf("CDR exceeds the record size limit")
		}
		var cdrHdr cdrFile.CdrHeader
		cdrHdr.CdrLength = uint16(len(cdrBytes))
		cdrHdr.DataRecordFormat = cdrFile.BasicEncodingRules
		tmpCdr := cdrFile.CDR{
			Hdr:     cdrHdr,
			CdrByte: cdrBytes,
		}
		cdrfile.CdrList = append(cdrfile.CdrList, tmpCdr)

		cdrfile.Hdr.FileLength += uint32(len(cdrBytes)) + 4
		if cdrHdr.ReleaseIdentifier == 7 {
			cdrfile.Hdr.FileLength++
		}
	}

	name := buildCdrFileName(ueid)
	temporary, err := os.CreateTemp(filepath.Dir(name), ".chf-cdr-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryName); removeErr != nil && !os.IsNotExist(removeErr) {
			logger.ChargingdataPostLog.Warnf("Remove temporary CDR: %v", removeErr)
		}
	}()
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = cdrfile.Encoding(temporaryName); err != nil {
		return fmt.Errorf("failed to write CDR file: %w", err)
	}
	return os.Rename(temporaryName, name)
}

// Leave room for partial sequence/closing metadata added when the segment closes.
const maxCDRContentSize = math.MaxUint16 - 32

// A transient change is built in memory; it is never saved as a DB snapshot.
type cdrChange struct {
	After     charging.CDRState
	Closed    []*cdrType.CHFRecord
	metadata  *charging.CDRMetadata
	replay    bool
	rotations int
	stamp     time.Time
}

func (p *Processor) prepareCDRChange(
	state *charging.CDRState, data models.Chf_ConvCharging_ChargingDataRequest,
	operation string, metadata *charging.CDRMetadata, replay bool,
) (*cdrChange, error) {
	if state.Closed || state.Subscriber != data.SubscriberIdentifier ||
		(operation != chargingUpdateOperation && operation != chargingReleaseOperation) {
		return nil, charging.ErrConflict
	}
	body, err := chargingdb.EncodeSnapshot(state)
	if err != nil {
		return nil, err
	}
	stamp, err := time.Parse(time.RFC3339Nano, metadata.Stamp)
	if err != nil {
		return nil, err
	}
	change := &cdrChange{metadata: metadata, replay: replay, stamp: stamp}
	if err = chargingdb.DecodeSnapshot(body, &change.After); err != nil {
		return nil, err
	}
	if err = p.UpdateCDR(change.After.Current, data); err != nil {
		return nil, err
	}
	if encoded, encodeErr := asn.BerMarshalWithParams(change.After.Current, "explicit,choice"); encodeErr != nil {
		return nil, encodeErr
	} else if len(encoded) > maxCDRContentSize {
		if err = chargingdb.DecodeSnapshot(body, &change.After); err != nil {
			return nil, err
		}
		for _, usage := range data.MultipleUnitUsage {
			piece := models.Chf_ConvCharging_ChargingDataRequest{
				MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{usage},
			}
			if err = p.appendCDRPiece(change, piece); err != nil {
				return nil, err
			}
		}
		for _, trigger := range data.Triggers {
			piece := models.Chf_ConvCharging_ChargingDataRequest{Triggers: []models.Chf_ConvCharging_Trigger{trigger}}
			if err = p.appendCDRPiece(change, piece); err != nil {
				return nil, err
			}
		}
	}
	if operation == chargingReleaseOperation {
		if err = p.CloseCDR(change.After.Current, false); err != nil {
			return nil, err
		}
		change.Closed = append(change.Closed, change.After.Current)
		change.After.Closed = true
	} else if metadata.Partial {
		if err = p.rotateCDR(change); err != nil {
			return nil, err
		}
	}
	if replay && change.rotations != len(metadata.LocalSequences) {
		return nil, charging.ErrConflict
	}
	return change, nil
}

func (p *Processor) appendCDRPiece(change *cdrChange, data models.Chf_ConvCharging_ChargingDataRequest) error {
	current := change.After.Current.ChargingFunctionRecord
	usageCount, triggerCount := len(current.ListOfMultipleUnitUsage), len(current.Triggers)
	if err := p.UpdateCDR(change.After.Current, data); err != nil {
		return err
	}
	encoded, err := asn.BerMarshalWithParams(change.After.Current, "explicit,choice")
	if err != nil {
		return err
	}
	if len(encoded) <= maxCDRContentSize {
		return nil
	}
	current.ListOfMultipleUnitUsage = current.ListOfMultipleUnitUsage[:usageCount]
	current.Triggers = current.Triggers[:triggerCount]
	if usageCount == 0 && triggerCount == 0 {
		return fmt.Errorf("one CDR usage or trigger exceeds the record size limit")
	}
	if err = p.rotateCDR(change); err != nil {
		return err
	}
	return p.appendCDRPiece(change, data)
}

func (p *Processor) rotateCDR(change *cdrChange) error {
	if err := p.CloseCDR(change.After.Current, true); err != nil {
		return err
	}
	change.Closed = append(change.Closed, change.After.Current)
	body, err := chargingdb.EncodeSnapshot(change.After.Current)
	if err != nil {
		return err
	}
	var next cdrType.CHFRecord
	if err = chargingdb.DecodeSnapshot(body, &next); err != nil {
		return err
	}
	record := next.ChargingFunctionRecord
	sequence := *record.RecordSequenceNumber
	if sequence == math.MaxInt64 {
		return fmt.Errorf("CDR sequence exhausted")
	}
	sequence++
	record.RecordSequenceNumber = &sequence
	record.ListOfMultipleUnitUsage, record.Triggers = nil, nil
	record.CauseForRecClosing.Value = 0
	record.RecordOpeningTime = cdrConvert.TimeStampToCdr(&change.stamp)
	if change.rotations == len(change.metadata.LocalSequences) {
		if change.replay {
			return charging.ErrConflict
		}
		self := chf_context.GetSelf()
		self.Lock()
		self.LocalRecordSequenceNumber++
		local := int64(self.LocalRecordSequenceNumber)
		self.Unlock()
		change.metadata.LocalSequences = append(change.metadata.LocalSequences, local)
	}
	record.LocalRecordSequenceNumber = &cdrType.LocalSequenceNumber{
		Value: change.metadata.LocalSequences[change.rotations],
	}
	change.rotations++
	change.After.Current = &next
	return nil
}

// Normal CDR processing has no DB calls. Finish persists only the compact replay
// metadata with the existing request-completion write. Pending requests stay fenced.
func (p *Processor) applyChargingCDR(ue *chf_context.ChfUe, record *charging.Record, partial bool) error {
	if record.State != charging.Processing && record.State != charging.Uncertain {
		return charging.ErrPending
	}
	progress := ue.CDRProgress[record.Session]
	if progress.EventID == record.ID {
		record.CDR = progress.Metadata
		return p.writeCDRs(ue.Supi, ue.Records)
	}
	metadata := record.CDR
	if metadata == nil {
		metadata = &charging.CDRMetadata{
			Previous: progress.EventID, Partial: partial, Stamp: record.CreatedAt.Format(time.RFC3339Nano),
		}
	}
	if metadata.Previous != progress.EventID {
		return charging.ErrConflict
	}
	var data models.Chf_ConvCharging_ChargingDataRequest
	if err := json.Unmarshal(record.Request, &data); err != nil {
		return err
	}
	if data.InvocationTimeStamp != nil && record.CDR == nil {
		metadata.Stamp = data.InvocationTimeStamp.Format(time.RFC3339Nano)
	}
	state := &charging.CDRState{
		Subscriber: ue.Supi, NotifyURI: ue.NotifyUri, Current: ue.Cdr[record.Session], Closed: progress.Closed,
	}
	change, err := p.prepareCDRChange(state, data, record.Operation, metadata, false)
	if err != nil {
		return err
	}
	if err = installCDRChange(ue, record.Session, record.ID, change); err != nil {
		return err
	}
	record.CDR = metadata
	return p.writeCDRs(ue.Supi, ue.Records)
}

func installCDRChange(ue *chf_context.ChfUe, ref, eventID string, change *cdrChange) error {
	records := append([]*cdrType.CHFRecord(nil), change.Closed...)
	records = append(records, change.After.Current)
	if err := mergeCDRRecords(ue, records); err != nil {
		return err
	}
	if ue.Cdr == nil {
		ue.Cdr = make(map[string]*cdrType.CHFRecord)
	}
	if ue.CDRProgress == nil {
		ue.CDRProgress = make(map[string]charging.CDRProgress)
	}
	ue.Cdr[ref] = change.After.Current
	ue.NotifyUri = change.After.NotifyURI
	ue.CDRProgress[ref] = charging.CDRProgress{
		EventID: eventID, Closed: change.After.Closed, Metadata: change.metadata,
	}
	return nil
}

// RestoreCDRs rebuilds from the initial seed and completed original requests.
// It replaces the projection; it never appends recovered usage to an old cache.
// The caller holds CULock. Pending accounting recovery remains separate.
func (p *Processor) RestoreCDRs(ue *chf_context.ChfUe) error {
	ctx, cancel := journalContext()
	defer cancel()
	histories, err := p.journal.LoadCDRHistory(ctx, ue.Supi)
	if err != nil {
		return err
	}
	if len(histories) == 0 {
		return fmt.Errorf("no initial CDR metadata for subscriber %s", ue.Supi)
	}
	rebuilt := &chf_context.ChfUe{Supi: ue.Supi}
	for _, history := range histories {
		initial := &cdrChange{After: history.Initial}
		if err = installCDRChange(rebuilt, history.Resource, "", initial); err != nil {
			return err
		}
		if err = p.replayCDRHistory(rebuilt, history); err != nil {
			return err
		}
	}
	if err = p.writeCDRs(ue.Supi, rebuilt.Records); err != nil {
		return err
	}
	ue.Cdr, ue.Records, ue.CDRProgress = rebuilt.Cdr, rebuilt.Records, rebuilt.CDRProgress
	ue.NotifyUri = rebuilt.NotifyUri
	// Avoid allocating an already-restored local number after process restart.
	self := chf_context.GetSelf()
	self.Lock()
	for _, record := range rebuilt.Records {
		local := record.ChargingFunctionRecord.LocalRecordSequenceNumber
		if local != nil && local.Value > 0 && uint64(local.Value) > self.LocalRecordSequenceNumber {
			self.LocalRecordSequenceNumber = uint64(local.Value)
		}
	}
	self.Unlock()
	return nil
}

func (p *Processor) replayCDRHistory(ue *chf_context.ChfUe, history charging.CDRHistory) error {
	next := make(map[string]charging.Record, len(history.Events))
	for _, event := range history.Events {
		if event.CDR == nil || event.ID != event.Identity.ID() || event.Session != history.Resource ||
			event.State != charging.Succeeded {
			return charging.ErrConflict
		}
		if _, exists := next[event.CDR.Previous]; exists {
			return charging.ErrConflict
		}
		next[event.CDR.Previous] = event
	}
	for range len(history.Events) {
		previous := ue.CDRProgress[history.Resource].EventID
		event, exists := next[previous]
		if !exists {
			return charging.ErrConflict
		}
		delete(next, previous)
		var data models.Chf_ConvCharging_ChargingDataRequest
		if err := json.Unmarshal(event.Request, &data); err != nil {
			return err
		}
		state := &charging.CDRState{
			Subscriber: ue.Supi, NotifyURI: history.Initial.NotifyURI, Current: ue.Cdr[history.Resource],
			Closed: ue.CDRProgress[history.Resource].Closed,
		}
		change, err := p.prepareCDRChange(state, data, event.Operation, event.CDR, true)
		if err != nil {
			return err
		}
		if err = installCDRChange(ue, history.Resource, event.ID, change); err != nil {
			return err
		}
	}
	return nil
}

func mergeCDRRecords(ue *chf_context.ChfUe, records []*cdrType.CHFRecord) error {
	byID := make(map[string]*cdrType.CHFRecord)
	for _, record := range append(append([]*cdrType.CHFRecord(nil), ue.Records...), records...) {
		id, err := charging.CDRIdentity(record)
		if err != nil {
			return err
		}
		byID[id] = record
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := byID[ids[i]].ChargingFunctionRecord, byID[ids[j]].ChargingFunctionRecord
		leftRef, rightRef := string(left.ChargingSessionIdentifier.Value), string(right.ChargingSessionIdentifier.Value)
		if leftRef != rightRef {
			return leftRef < rightRef
		}
		leftSequence, rightSequence := int64(0), int64(0)
		if left.RecordSequenceNumber != nil {
			leftSequence = *left.RecordSequenceNumber
		}
		if right.RecordSequenceNumber != nil {
			rightSequence = *right.RecordSequenceNumber
		}
		return leftSequence < rightSequence
	})
	ue.Records = make([]*cdrType.CHFRecord, 0, len(ids))
	for _, id := range ids {
		ue.Records = append(ue.Records, byID[id])
	}
	return nil
}

func (p *Processor) writeCDRs(subscriber string, records []*cdrType.CHFRecord) error {
	if p.cdrWriter != nil {
		return p.cdrWriter(subscriber, records)
	}
	return dumpCdrFile(subscriber, records)
}
