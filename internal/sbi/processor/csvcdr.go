package processor

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/free5gc/chf/cdr/asn"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/openapi/models"
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

type ChargingDataAction int

const (
	CDRCreate ChargingDataAction = iota
	CDRUpdate
	CDRRelease
)

func dumpCdrToCSV(ueid string, records []*cdrType.CHFRecord, action ChargingDataAction, timeStamp time.Time) error {
	file, err := os.OpenFile("/tmp/"+"cdr"+".csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			fmt.Printf("failed to close file: %v\n", cerr)
		}
	}()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	fi, err := file.Stat() // Write header only if file is new
	if err == nil && fi.Size() == 0 {
		headers := []string{
			"RecordType",
			"RecordingNetworkFunctionID",
			"RecordOpeningTime",
			"StartTime",
			"EndTime",
			"ActionType",
			"Duration",
			"LocalRecordSequenceNumber",
			"SubscriptionIDType",
			"SubscriptionIDData",
			"ChargingSessionIdentifier_sessionId",
			"ChargingID",
			"ConsumerName",
			"ConsumerV4Addr",
			"ConsumerV6Addr",
			// "ConsumerFqdn",
			"ConsumerPlmnId",
			"ConsumerNetworkFunctionality",
			// "ServiceSpecificationInformation",
			// "RegistrationMessagetype",
			"PDUSessionChargingID",
			"PDUSessionId",
			"NetworkSliceInstanceID_SST",
			"NetworkSliceInstanceID_SD",
			"DataNetworkNameIdentifier",
			"RatingGroup",
			"cellID",
			"PLMNID",
			"TAC",
			"SelectionMode",
			// "UPFID",
			"PduType",
			"PduIPV4Address",
			"PduIPV4dynamicAddressFlag",
			"PduIPV6Address",
			"PduIPV6dynamicPrefixFlag",
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
		if err = writer.Write(headers); err != nil {
			return err
		}
	}

	for _, record := range records {
		chfCdr := record.ChargingFunctionRecord

		recordType := strconv.Itoa(int(chfCdr.RecordType.Value))
		recordingNetworkFunctionID := string(chfCdr.RecordingNetworkFunctionID.Value)
		recordOpeningTime := decodeRecordOpeningTime(chfCdr.RecordOpeningTime.Value)

		var actionType, startTime, endTime string
		switch action {
		case CDRCreate:
			actionType = "Create"
			startTime = timeStamp.UTC().Format(time.RFC3339)
		case CDRUpdate:
			actionType = "Update"
		case CDRRelease:
			actionType = "Release"
			endTime = timeStamp.UTC().Format(time.RFC3339)
		default:
			return fmt.Errorf("unknown action type: %d", action)
		}

		duration := strconv.Itoa(int(chfCdr.Duration.Value))
		localRecordSequenceNumber := strconv.Itoa(int(chfCdr.LocalRecordSequenceNumber.Value))
		subscriptionIDType := getSubscriptionIDTypeName(chfCdr.SubscriberIdentifier.SubscriptionIDType.Value)
		subscriptionIDData := string(chfCdr.SubscriberIdentifier.SubscriptionIDData)
		chargingSessionID := string(chfCdr.ChargingSessionIdentifier.Value)
		chargingID := strconv.Itoa(int(chfCdr.ChargingID.Value))
		consumerName := string(chfCdr.NFunctionConsumerInformation.NetworkFunctionName.Value)

		var ipTextV4Address, ipTextV6Address string
		// var consumerFQDN string
		if chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv4Address != nil {
			ipTextV4Address = string(*chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv4Address.IPTextV4Address)
		} else {
			ipTextV4Address = ""
		}
		if chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv6Address != nil {
			ipTextV6Address = string(*chfCdr.NFunctionConsumerInformation.NetworkFunctionIPv6Address.IPTextV6Address)
		} else {
			ipTextV6Address = ""
		}
		// if chfCdr.NFunctionConsumerInformation.NetworkFunctionFQDN != nil {
		// 	consumerFQDN = string(*chfCdr.NFunctionConsumerInformation.NetworkFunctionFQDN.DomainName)
		// } else {
		// 	consumerFQDN = ""
		// }

		consumerPlmnID := CdrToPlmnId(*chfCdr.NFunctionConsumerInformation.NetworkFunctionPLMNIdentifier)
		consumerNetworkFunctionality := getNetworkFunctionality(
			chfCdr.NFunctionConsumerInformation.NetworkFunctionality.Value,
		)
		// serviceSpecificationInfo := getServiceSpecificationInformation(chfCdr.ServiceSpecificationInformation)
		// registrationMessageType := getRegistrationMessageTypeCheck(chfCdr.RegistrationChargingInformation)
		pduSessionChargingID := strconv.Itoa(int(chfCdr.PDUSessionChargingInformation.PDUSessionChargingID.Value))
		pduSessionID := strconv.Itoa(int(chfCdr.PDUSessionChargingInformation.PDUSessionId.Value))
		networkSliceInstanceID_SST := strconv.Itoa(int(chfCdr.PDUSessionChargingInformation.NetworkSliceInstanceID.SST.Value))
		networkSliceInstanceID_SD := string(chfCdr.PDUSessionChargingInformation.NetworkSliceInstanceID.SD.Value)
		dataNetworkNameIdentifier := string(chfCdr.PDUSessionChargingInformation.DataNetworkNameIdentifier.Value)
		pduType := getPduType(chfCdr.PDUSessionChargingInformation.PDUType.Value)
		PduIPV4Address, PduIPV6Address, PduIPV4dynamicAddressFlag, PduIPV6dynamicPrefixFlag := getPduIPAddresses(
			chfCdr.PDUSessionChargingInformation.PDUAddress,
		)
		var totalVolume, uplinkVolume, downlinkVolume int64
		var ratingGroup string
		// var upfid string
		for _, multiUnitUsage := range chfCdr.ListOfMultipleUnitUsage {
			detailsList := extractUsedUnitContainerDetails(multiUnitUsage.UsedUnitContainers)
			ratingGroup = strconv.FormatInt(multiUnitUsage.RatingGroup.Value, 10)
			// upfid = string(multiUnitUsage.UPFID.Value)

			for _, details := range detailsList {
				totalVolume, err = strconv.ParseInt(details.TotalVolume, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid TotalVolume %q: %w", details.TotalVolume, err)
				}
				uplinkVolume, err = strconv.ParseInt(details.UplinkVolume, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid TotalVolume %q: %w", details.TotalVolume, err)
				}
				downlinkVolume, err = strconv.ParseInt(details.DownlinkVolume, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid TotalVolume %q: %w", details.TotalVolume, err)
				}
			}
		}

		var cellID, plmnID, tac string
		if chfCdr.PDUSessionChargingInformation.UserLocationInformation != nil {
			var userLocationinfo models.UserLocation
			if err := json.Unmarshal(
				chfCdr.PDUSessionChargingInformation.UserLocationInformation.Value,
				&userLocationinfo,
			); err != nil {
				return fmt.Errorf("failed to unmarshal UserLocationInformation: %v", err)
			}
			if err = json.Unmarshal(
				chfCdr.PDUSessionChargingInformation.UserLocationInformation.Value,
				&userLocationinfo,
			); err != nil {
				return fmt.Errorf(
					"failed to unmarshal UserLocationInformation: %v",
					err,
				)
			}
			if userLocationinfo.NrLocation != nil {
				if userLocationinfo.NrLocation.Ncgi != nil {
					cellID = userLocationinfo.NrLocation.Ncgi.NrCellId
					plmnID = userLocationinfo.NrLocation.Ncgi.PlmnId.Mcc + userLocationinfo.NrLocation.Ncgi.PlmnId.Mnc
				}
				if userLocationinfo.NrLocation.Tai != nil {
					tac = userLocationinfo.NrLocation.Tai.Tac
				}
			}
			if userLocationinfo.EutraLocation != nil {
				if userLocationinfo.EutraLocation.Ecgi != nil {
					cellID = userLocationinfo.EutraLocation.Ecgi.EutraCellId
					plmnID = userLocationinfo.EutraLocation.Ecgi.PlmnId.Mcc + userLocationinfo.EutraLocation.Ecgi.PlmnId.Mnc
				}
				if userLocationinfo.EutraLocation.Tai != nil {
					tac = userLocationinfo.EutraLocation.Tai.Tac
				}
			}
		}
		var selectionmode string
		if chfCdr.PDUSessionChargingInformation.ChChSelectionMode != nil {
			switch chfCdr.PDUSessionChargingInformation.ChChSelectionMode.Value {
			case cdrType.ChChSelectionModePresentHomeDefault:
				selectionmode = "HOME_DEFAULT"
			case cdrType.ChChSelectionModePresentRoamingDefault:
				selectionmode = "ROAMING_DEFAULT"
			case cdrType.ChChSelectionModePresentVisitingDefault:
				selectionmode = "VISITING_DEFAULT"
			}
		}

		row := []string{
			recordType,
			recordingNetworkFunctionID,
			recordOpeningTime,
			startTime,
			endTime,
			actionType,
			duration,
			localRecordSequenceNumber,
			subscriptionIDType,
			subscriptionIDData,
			chargingSessionID,
			chargingID,
			consumerName,
			ipTextV4Address,
			ipTextV6Address,
			// consumerFQDN,
			consumerPlmnID,
			consumerNetworkFunctionality,
			// serviceSpecificationInfo,
			// registrationMessageType,
			pduSessionChargingID,
			pduSessionID,
			networkSliceInstanceID_SST,
			networkSliceInstanceID_SD,
			dataNetworkNameIdentifier,
			ratingGroup,
			cellID,
			plmnID,
			tac,
			selectionmode,
			// upfid,
			pduType,
			PduIPV4Address,
			PduIPV4dynamicAddressFlag,
			PduIPV6Address,
			PduIPV6dynamicPrefixFlag,
			strconv.FormatInt(totalVolume, 10),
			strconv.FormatInt(uplinkVolume, 10),
			strconv.FormatInt(downlinkVolume, 10),
		}
		if err = writer.Write(row); err != nil {
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

func getPduType(value asn.Enumerated) string {
	switch value {
	case 0:
		return "IPV4V6"
	case 1:
		return "IPV4"
	case 2:
		return "IPV6"
	case 3:
		return "Unstructured"
	case 4:
		return "Ethernet"
	default:
		return ""
	}
}

// func getServiceSpecificationInformation(info *asn.OctetString) string {
// 	if info != nil {
// 		return string(*info)
// 	}
// 	return ""
// }

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

// func getRegistrationMessageType(value asn.Enumerated) string {
// 	switch value {
// 	case 0:
// 		return "Initial"
// 	case 1:
// 		return "Mobility"
// 	case 2:
// 		return "Periodic"
// 	case 3:
// 		return "Emergency"
// 	case 4:
// 		return "Deregistration"
// 	default:
// 		return ""
// 	}
// }

// func getRegistrationMessageTypeCheck(info *cdrType.RegistrationChargingInformation) string {
// 	if info == nil {
// 		return ""
// 	}
// 	return getRegistrationMessageType(info.RegistrationMessagetype.Value)
// }

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

func getPduIPAddresses(addr *cdrType.PDUAddress) (ipv4, ipv6, ipv4Flag, ipv6Flag string) {
	if addr != nil {
		if addr.PDUIPv4Address != nil && addr.PDUIPv4Address.IPTextV4Address != nil {
			ipv4 = string(*addr.PDUIPv4Address.IPTextV4Address)
		}
		if addr.PDUIPv6AddresswithPrefix != nil && addr.PDUIPv6AddresswithPrefix.IPTextV6Address != nil {
			ipv6 = string(*addr.PDUIPv6AddresswithPrefix.IPTextV6Address)
		}
		if addr.IPV4dynamicAddressFlag != nil {
			if addr.IPV4dynamicAddressFlag.Value {
				ipv4Flag = "True"
			} else {
				ipv4Flag = "False"
			}
		}
		if addr.IPV6dynamicPrefixFlag != nil {
			if addr.IPV6dynamicPrefixFlag.Value {
				ipv6Flag = "True"
			} else {
				ipv6Flag = "False"
			}
		}
	}
	return ipv4, ipv6, ipv4Flag, ipv6Flag
}
