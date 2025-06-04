package processor

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/free5gc/chf/cdr/asn"
	"github.com/free5gc/chf/cdr/cdrType"
)

type UsedUnitContainerDetails struct {
	TotalVolume    string
	UplinkVolume   string
	DownlinkVolume string
	// TimeOfFirstUsage    string
	// TimeOfLastUsage     string
	// CallDuration        string
	// MaxBitrateUL        string
	// MaxBitrateDL        string
	// GuaranteedBitrateUL string
	// GuaranteedBitrateDL string
	// UserLocationInfo    string
	// RATType             string
}

func dumpCdrToCSV(ueid string, records []*cdrType.CHFRecord) error {
	file, err := os.Create("/tmp/" + ueid + ".csv")
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	headers := []string{
		"RecordType",
		"RecordingNetworkFunctionID",
		"RecordOpeningTime",
		"Duration",
		"LocalRecordSequenceNumber",
		"SubscriptionIDType",
		"SubscriptionIDData",
		"ChargingSessionIdentifier_sessionId",
		"ChargingID",
		"ConsumerName",
		"ConsumerV4Addr",
		"ConsumerV6Addr",
		"ConsumerFqdn",
		"ConsumerPlmnId",
		"ConsumerNetworkFunctionality",
		"ServiceSpecificationInformation",
		"RegistrationMessagetype",
		"PDUSessionChargingID",
		"PDUSessionId",
		"NetworkSliceInstanceID_SST",
		"NetworkSliceInstanceID_SD",
		"DataNetworkNameIdentifier",
		"RatingGroup",
		"UPFID",
		"TotalVolume",
		"UplinkVolume",
		"DownlinkVolume",
		// "TimeOfFirstUsage",
		// "TimeOfLastUsage",
		// "CallDuration",
		// "MaxBitrateUL",
		// "MaxBitrateDL",
		// "GuaranteedBitrateUL",
		// "GuaranteedBitrateDL",
		// "UserLocationInfo",
		// "RATType",
	}
	if err := writer.Write(headers); err != nil {
		return err
	}

	for _, record := range records {
		chfCdr := record.ChargingFunctionRecord

		recordType := strconv.Itoa(int(chfCdr.RecordType.Value))
		recordingNetworkFunctionID := string(chfCdr.RecordingNetworkFunctionID.Value)
		recordOpeningTime := decodeRecordOpeningTime(chfCdr.RecordOpeningTime.Value)
		duration := strconv.Itoa(int(chfCdr.Duration.Value))
		localRecordSequenceNumber := strconv.Itoa(int(chfCdr.LocalRecordSequenceNumber.Value))
		subscriptionIDType := getSubscriptionIDTypeName(chfCdr.SubscriberIdentifier.SubscriptionIDType.Value)
		subscriptionIDData := string(chfCdr.SubscriberIdentifier.SubscriptionIDData)
		chargingSessionID := string(chfCdr.ChargingSessionIdentifier.Value)
		chargingID := strconv.Itoa(int(chfCdr.ChargingID.Value))
		consumerName := string(chfCdr.NFunctionConsumerInformation.NetworkFunctionName.Value)

		var ipTextV4Address, ipTextV6Address, consumerFQDN string
		if chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv4Address != nil {
			ipTextV4Address = string(*chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv4Address.IPTextV4Address)
		} else {
			ipTextV4Address = "UNKNOWN"
		}
		if chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv6Address != nil {
			ipTextV6Address = string(*chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv6Address.IPTextV6Address)
		} else {
			ipTextV6Address = "UNKNOWN"
		}
		if chfCdr.NFunctionConsumerInformation.NetworkFunctionFQDN != nil {
			consumerFQDN = string(*chfCdr.NFunctionConsumerInformation.NetworkFunctionFQDN.DomainName)
		} else {
			consumerFQDN = "UNKNOWN"
		}

		consumerPlmnID := CdrToPlmnId(*chfCdr.NFunctionConsumerInformation.NetworkFunctionPLMNIdentifier)
		consumerNetworkFunctionality := getNetworkFunctionality(chfCdr.NFunctionConsumerInformation.NetworkFunctionality.Value)
		serviceSpecificationInfo := getServiceSpecificationInformation(chfCdr.ServiceSpecificationInformation)
		registrationMessageType := getRegistrationMessageTypeCheck(chfCdr.RegistrationChargingInformation)
		pduSessionChargingID := strconv.Itoa(int(chfCdr.PDUSessionChargingInformation.PDUSessionChargingID.Value))
		pduSessionID := strconv.Itoa(int(chfCdr.PDUSessionChargingInformation.PDUSessionId.Value))
		networkSliceInstanceID_SST := strconv.Itoa(int(chfCdr.PDUSessionChargingInformation.NetworkSliceInstanceID.SST.Value))
		networkSliceInstanceID_SD := string(chfCdr.PDUSessionChargingInformation.NetworkSliceInstanceID.SD.Value)
		dataNetworkNameIdentifier := string(chfCdr.PDUSessionChargingInformation.DataNetworkNameIdentifier.Value)

		var totalVolume, uplinkVolume, downlinkVolume int64
		var ratingGroup, upfid string
		for _, multiUnitUsage := range chfCdr.ListOfMultipleUnitUsage {
			detailsList := extractUsedUnitContainerDetails(multiUnitUsage.UsedUnitContainers)
			ratingGroup = strconv.FormatInt(multiUnitUsage.RatingGroup.Value, 10)
			upfid = string(multiUnitUsage.UPFID.Value)

			for _, details := range detailsList {
				totalVolume, _ = strconv.ParseInt(details.TotalVolume, 10, 64)
				uplinkVolume, _ = strconv.ParseInt(details.UplinkVolume, 10, 64)
				downlinkVolume, _ = strconv.ParseInt(details.DownlinkVolume, 10, 64)
			}
		}

		row := []string{
			recordType,
			recordingNetworkFunctionID,
			recordOpeningTime,
			duration,
			localRecordSequenceNumber,
			subscriptionIDType,
			subscriptionIDData,
			chargingSessionID,
			chargingID,
			consumerName,
			ipTextV4Address,
			ipTextV6Address,
			consumerFQDN,
			consumerPlmnID,
			consumerNetworkFunctionality,
			serviceSpecificationInfo,
			registrationMessageType,
			pduSessionChargingID,
			pduSessionID,
			networkSliceInstanceID_SST,
			networkSliceInstanceID_SD,
			dataNetworkNameIdentifier,
			ratingGroup,
			upfid,
			strconv.FormatInt(totalVolume, 10),
			strconv.FormatInt(uplinkVolume, 10),
			strconv.FormatInt(downlinkVolume, 10),
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}

	return nil
}

func getSubscriptionIDTypeName(value asn.Enumerated) string {

	switch value {
	case 0:
		return "164"
	case 1:
		return "IMSI"
	case 2:
		return "SIPURI"
	case 3:
		return "NAI"
	case 4:
		return "PRIVATE"
	default:
		return ""
	}
}

func getServiceSpecificationInformation(info *asn.OctetString) string {
	if info != nil {
		return string(*info)
	}
	return ""
}

func getNetworkFunctionality(value asn.Enumerated) string {
	switch value {
	case 0:
		return "CHF"
	case 1:
		return "SMF"
	case 2:
		return "AMF"
	case 3:
		return "SMSF"
	case 4:
		return "SGW"
	case 5:
		return "ISMF"
	case 6:
		return "EPDG"
	case 7:
		return "CEF"
	case 8:
		return "NEF"
	case 9:
		return "PGWCSMF"
	case 10:
		return "MnSProducer"
	default:
		return ""
	}
}

func getRegistrationMessageType(value asn.Enumerated) string {
	switch value {
	case 0:
		return "Initial"
	case 1:
		return "Mobility"
	case 2:
		return "Periodic"
	case 3:
		return "Emergency"
	case 4:
		return "Deregistration"
	default:
		return ""
	}
}

func getRegistrationMessageTypeCheck(info *cdrType.RegistrationChargingInformation) string {
	if info == nil {
		return ""
	}
	return getRegistrationMessageType(info.RegistrationMessagetype.Value)
}

func decodeRecordOpeningTime(value asn.OctetString) string {
	if value == nil || len(value) < 9 {
		return ""
	}

	year := 2000 + int((value[0]>>4)*10+(value[0]&0x0F))
	month := time.Month((value[1]>>4)*10 + (value[1] & 0x0F))
	day := int((value[2]>>4)*10 + (value[2] & 0x0F))
	hour := int((value[3]>>4)*10 + (value[3] & 0x0F))
	minute := int((value[4]>>4)*10 + (value[4] & 0x0F))
	second := int((value[5]>>4)*10 + (value[5] & 0x0F))

	tzSign := 1
	if value[6] == '-' {
		tzSign = -1
	}
	tzHour := int((value[7]>>4)*10 + (value[7] & 0x0F))
	tzMinute := int((value[8]>>4)*10 + (value[8] & 0x0F))
	tzOffset := tzSign * (tzHour*3600 + tzMinute*60) // Time zone offset in seconds

	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59 {
		return "INVALID_TIME"
	}

	location := time.FixedZone("CustomTZ", tzOffset)
	parsedTime := time.Date(year, month, day, hour, minute, second, 0, location)

	return parsedTime.Format(time.RFC3339)
}
func CdrToPlmnId(cdrPlmnId cdrType.PLMNId) string {
	if len(cdrPlmnId.Value) != 3 {
		fmt.Println("Invalid PLMNId length")
		return "INVALID_PLMN"
	}

	mcc := string([]byte{
		(cdrPlmnId.Value[0] & 0x0F) + '0',
		(cdrPlmnId.Value[0] >> 4) + '0',
		(cdrPlmnId.Value[1] & 0x0F) + '0',
	})

	var mnc string
	if (cdrPlmnId.Value[1] >> 4) == 0x0F {
		mnc = string([]byte{
			(cdrPlmnId.Value[2] & 0x0F) + '0',
			(cdrPlmnId.Value[2] >> 4) + '0',
		})
	} else { // If the MNC is 3 digits
		mnc = string([]byte{
			(cdrPlmnId.Value[2] & 0x0F) + '0',
			(cdrPlmnId.Value[2] >> 4) + '0',
			(cdrPlmnId.Value[1] >> 4) + '0',
		})
	}

	return mcc + mnc
}

func extractUsedUnitContainerDetails(usedUnitContainers []cdrType.UsedUnitContainer) []UsedUnitContainerDetails {
	var detailsList []UsedUnitContainerDetails

	for _, container := range usedUnitContainers {
		totalVolume := strconv.FormatInt(container.DataTotalVolume.Value, 10)
		uplinkVolume := strconv.FormatInt(container.DataVolumeUplink.Value, 10)
		downlinkVolume := strconv.FormatInt(container.DataVolumeDownlink.Value, 10)
		// timeOfFirstUsage := decodeRecordOpeningTime(container.PDUContainerInformation.TimeOfFirstUsage.Value)
		// timeOfLastUsage := decodeRecordOpeningTime(container.PDUContainerInformation.TimeOfLastUsage.Value)
		// callDuration := safeInt64(container.Time)
		// maxbitrateUL := safeString(&container.PDUContainerInformation.QoSInformation.MaxbitrateUL.Value)
		// maxbitrateDL := safeString(&container.PDUContainerInformation.QoSInformation.MaxbitrateDL.Value)
		// guaranteedbitrateUL := safeString(&container.PDUContainerInformation.QoSInformation.GuaranteedbitrateUL.Value)
		// guaranteedbitrateDL := safeString(&container.PDUContainerInformation.QoSInformation.GuaranteedbitrateDL.Value)
		// userLocationInformation := safeString(&container.PDUContainerInformation.UserLocationInformation.Value)
		// rATType := strconv.FormatInt(container.PDUContainerInformation.RATType.Value, 10)

		// Create a struct instance and populate it
		details := UsedUnitContainerDetails{
			TotalVolume:    totalVolume,
			UplinkVolume:   uplinkVolume,
			DownlinkVolume: downlinkVolume,
			// TimeOfFirstUsage:    timeOfFirstUsage,
			// TimeOfLastUsage:     timeOfLastUsage,
			// CallDuration:        callDuration,
			// MaxBitrateUL:        maxbitrateUL,
			// MaxBitrateDL:        maxbitrateDL,
			// GuaranteedBitrateUL: guaranteedbitrateUL,
			// GuaranteedBitrateDL: guaranteedbitrateDL,
			// UserLocationInfo:    userLocationInformation,
			// RATType:             rATType,
		}
		detailsList = append(detailsList, details)
	}

	return detailsList
}
