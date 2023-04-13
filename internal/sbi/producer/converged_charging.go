package producer

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	rate_datatype "github.com/free5gc/RatingUtil/dataType"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/free5gc/CDRUtil/cdrType"
	"github.com/free5gc/RatingUtil/dataType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/ftp"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/rating"
	"github.com/free5gc/chf/internal/util"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/httpwrapper"
	"github.com/free5gc/util/mongoapi"
	"go.mongodb.org/mongo-driver/bson"
)

const chargingDataColl = "chargingData"

func NotifyRecharge(ueId string, rg int) {
	var reauthorizationDetails []models.ReauthorizationDetails

	self := chf_context.CHF_Self()
	ue, ok := self.ChfUeFindBySupi(ueId)
	if !ok {
		logger.NotifyEventLog.Errorf("Do not find charging data for UE: %s", ueId)
		return
	}
	reauthorizationDetails = append(reauthorizationDetails, models.ReauthorizationDetails{
		RatingGroup: int32(rg),
	})

	notifyRequest := models.ChargingNotifyRequest{
		ReauthorizationDetails: reauthorizationDetails,
	}

	SendChargingNotification(ue.NotifyUri, notifyRequest)
}

func SendChargingNotification(notifyUri string, notifyRequest models.ChargingNotifyRequest) {
	client := util.GetNchfChargingNotificationCallbackClient()
	logger.NotifyEventLog.Warn("Send Charging Notification to SMF: uri: ", notifyUri)
	httpResponse, err := client.DefaultCallbackApi.ChargingNotification(context.Background(), notifyUri, notifyRequest)
	if err != nil {
		if httpResponse != nil {
			logger.NotifyEventLog.Warnf("Charging Notification Error[%s]", httpResponse.Status)
		} else {
			logger.NotifyEventLog.Warnf("Charging Notification Failed[%s]", err.Error())
		}
		return
	} else if httpResponse == nil {
		logger.NotifyEventLog.Warnln("Charging Notification[HTTP Response is nil]")
		return
	}
	defer func() {
		if resCloseErr := httpResponse.Body.Close(); resCloseErr != nil {
			logger.NotifyEventLog.Errorf("NFInstancesStoreApi response body cannot close: %+v", resCloseErr)
		}
	}()
	if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusNoContent {
		logger.NotifyEventLog.Warnf("Charging Notification Failed")
	} else {
		logger.NotifyEventLog.Tracef("Charging Notification Success")
	}
}

func HandleChargingdataInitial(request *httpwrapper.Request) *httpwrapper.Response {
	logger.ChargingdataPostLog.Infof("HandleChargingdataInitial")
	chargingdata := request.Body.(models.ChargingDataRequest)

	response, locationURI, problemDetails := ChargingDataCreate(chargingdata)
	respHeader := make(http.Header)
	respHeader.Set("Location", locationURI)

	if response != nil {
		return httpwrapper.NewResponse(http.StatusCreated, respHeader, response)
	} else if problemDetails != nil {
		return httpwrapper.NewResponse(int(problemDetails.Status), nil, problemDetails)
	}
	problemDetails = &models.ProblemDetails{
		Status: http.StatusForbidden,
		Cause:  "UNSPECIFIED",
	}
	return httpwrapper.NewResponse(http.StatusForbidden, nil, problemDetails)
}

func HandleChargingdataUpdate(request *httpwrapper.Request) *httpwrapper.Response {
	logger.ChargingdataPostLog.Infof("HandleChargingdataUpdate")
	chargingdata := request.Body.(models.ChargingDataRequest)
	chargingSessionId := request.Params["ChargingDataRef"]

	response, problemDetails := ChargingDataUpdate(chargingdata, chargingSessionId)

	if response != nil {
		return httpwrapper.NewResponse(http.StatusOK, nil, response)
	} else if problemDetails != nil {
		return httpwrapper.NewResponse(int(problemDetails.Status), nil, problemDetails)
	}
	problemDetails = &models.ProblemDetails{
		Status: http.StatusForbidden,
		Cause:  "UNSPECIFIED",
	}
	return httpwrapper.NewResponse(http.StatusForbidden, nil, problemDetails)
}

func HandleChargingdataRelease(request *httpwrapper.Request) *httpwrapper.Response {
	logger.ChargingdataPostLog.Infof("HandleChargingdateRelease")
	chargingdata := request.Body.(models.ChargingDataRequest)
	chargingSessionId := request.Params["ChargingDataRef"]

	problemDetails := ChargingDataRelease(chargingdata, chargingSessionId)

	if problemDetails == nil {
		return httpwrapper.NewResponse(http.StatusNoContent, nil, nil)
	} else if problemDetails != nil {
		return httpwrapper.NewResponse(int(problemDetails.Status), nil, problemDetails)
	}
	problemDetails = &models.ProblemDetails{
		Status: http.StatusForbidden,
		Cause:  "UNSPECIFIED",
	}
	return httpwrapper.NewResponse(http.StatusForbidden, nil, problemDetails)
}

func ChargingDataCreate(chargingData models.ChargingDataRequest) (*models.ChargingDataResponse,
	string, *models.ProblemDetails) {
	var responseBody models.ChargingDataResponse
	var chargingSessionId string

	self := chf_context.CHF_Self()
	ueId := chargingData.SubscriberIdentifier

	// Open CDR
	// ChargingDataRef(charging session id):
	// A unique identifier for a charging data resource in a PLMN
	// TODO determine charging session id(string type) supi+consumerid+localseq?
	ue, err := self.NewCHFUe(ueId)
	if err != nil {
		logger.ChargingdataPostLog.Errorf("New CHFUe error %s", err)
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}

	ue.CULock.Lock()
	defer ue.CULock.Unlock()

	ue.NotifyUri = chargingData.NotifyUri

	consumerId := chargingData.NfConsumerIdentification.NFName
	if !chargingData.OneTimeEvent {
		chargingSessionId = ueId + consumerId + strconv.Itoa(int(self.LocalRecordSequenceNumber))
	}
	cdr, err := OpenCDR(chargingData, ue, chargingSessionId, false)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}

	err = UpdateCDR(cdr, chargingData)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}

	ue.Cdr[chargingSessionId] = cdr

	if chargingData.OneTimeEvent {
		err = CloseCDR(cdr, false)
		if err != nil {
			problemDetails := &models.ProblemDetails{
				Status: http.StatusBadRequest,
			}
			return nil, "", problemDetails
		}
	}

	// CDR Transfer
	err = ftp.SendCDR(chargingData.SubscriberIdentifier)
	if err != nil {
		logger.ChargingdataPostLog.Error("FTP err", err)
	}

	logger.ChargingdataPostLog.Infof("Open CDR for UE %s", ueId)

	// build response
	logger.ChargingdataPostLog.Infof("NewChfUe %s", ueId)
	locationURI := self.GetIPv4Uri() + "/nchf-convergedcharging/v3/chargingdata/" + chargingSessionId
	timeStamp := time.Now()

	responseBody.InvocationTimeStamp = &timeStamp
	responseBody.InvocationSequenceNumber = chargingData.InvocationSequenceNumber

	return &responseBody, locationURI, nil
}

func ChargingDataUpdate(chargingData models.ChargingDataRequest, chargingSessionId string) (*models.ChargingDataResponse,
	*models.ProblemDetails) {
	var records []*cdrType.CHFRecord

	self := chf_context.CHF_Self()
	ueId := chargingData.SubscriberIdentifier
	ue, ok := self.ChfUeFindBySupi(ueId)
	if !ok {
		logger.ChargingdataPostLog.Errorf("Do not find  CHFUe[%s] error", ueId)
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, problemDetails
	}

	ue.CULock.Lock()
	defer ue.CULock.Unlock()

	// Online charging: Rate, Account, Reservation
	responseBody, partialRecord := BuildOnlineChargingDataUpdateResopone(chargingData)

	cdr := ue.Cdr[chargingSessionId]
	err := UpdateCDR(cdr, chargingData)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, problemDetails
	}

	if partialRecord {
		ueId := chargingData.SubscriberIdentifier

		CloseCDR(cdr, partialRecord)
		err = dumpCdrFile(ueId, []*cdrType.CHFRecord{cdr})
		if err != nil {
			problemDetails := &models.ProblemDetails{
				Status: http.StatusBadRequest,
			}
			return nil, problemDetails
		}

		OpenCDR(chargingData, ue, chargingSessionId, partialRecord)
		logger.ChargingdataPostLog.Tracef("CDR Record Sequence Number after Reopen %+v", *cdr.ChargingFunctionRecord.RecordSequenceNumber)
	}

	for _, cdr := range ue.Cdr {
		records = append(records, cdr)
	}
	err = dumpCdrFile(ueId, records)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, problemDetails
	}

	err = ftp.SendCDR(chargingData.SubscriberIdentifier)
	if err != nil {
		logger.ChargingdataPostLog.Error("FTP err", err)
	}

	timeStamp := time.Now()
	responseBody.InvocationTimeStamp = &timeStamp
	responseBody.InvocationSequenceNumber = chargingData.InvocationSequenceNumber

	return &responseBody, nil
}

func ChargingDataRelease(chargingData models.ChargingDataRequest, chargingSessionId string) *models.ProblemDetails {
	self := chf_context.CHF_Self()
	ueId := chargingData.SubscriberIdentifier
	ue, ok := self.ChfUeFindBySupi(ueId)
	if !ok {
		logger.ChargingdataPostLog.Errorf("Do not find CHFUe[%s] error", ueId)
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return problemDetails
	}

	ue.CULock.Lock()
	defer ue.CULock.Unlock()

	cdr := ue.Cdr[chargingSessionId]

	err := UpdateCDR(cdr, chargingData)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return problemDetails
	}

	err = CloseCDR(cdr, false)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return problemDetails
	}

	err = dumpCdrFile(ueId, []*cdrType.CHFRecord{cdr})
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return problemDetails
	}

	return nil
}

func BuildOnlineChargingDataCreateResopone(ue *chf_context.ChfUe, chargingData models.ChargingDataRequest) models.ChargingDataResponse {
	logger.ChargingdataPostLog.Info("In Build Online Charging Data Create Resopone")
	ue.NotifyUri = chargingData.NotifyUri

	multipleUnitInformation, _ := sessionChargingReservation(chargingData)

	responseBody := models.ChargingDataResponse{
		MultipleUnitInformation: multipleUnitInformation,
	}

	return responseBody
}

func BuildOnlineChargingDataUpdateResopone(chargingData models.ChargingDataRequest) (models.ChargingDataResponse, bool) {
	var partialRecord bool

	logger.ChargingdataPostLog.Info("In BuildOnlineChargingDataUpdateResopone ")

	multipleUnitInformation, partialRecord := sessionChargingReservation(chargingData)

	responseBody := models.ChargingDataResponse{
		MultipleUnitInformation: multipleUnitInformation,
	}

	return responseBody, partialRecord
}

// 32.296 6.2.2.3.1: Service usage request method with reservation
func sessionChargingReservation(chargingData models.ChargingDataRequest) ([]models.MultipleUnitInformation, bool) {
	var multipleUnitInformation []models.MultipleUnitInformation
	var addPDULimit bool
	var partialRecord bool
	var subscriberIdentifier *dataType.SubscriptionId

	self := chf_context.CHF_Self()
	supi := chargingData.SubscriberIdentifier

	ue, ok := self.ChfUeFindBySupi(supi)
	if !ok {
		logger.ChargingdataPostLog.Warnf("Do not find UE[%s]", supi)
		return nil, false
	}

	supiType := strings.Split(supi, "-")[0]
	switch supiType {
	case "imsi":
		subscriberIdentifier = &dataType.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_IMSI,
			SubscriptionIdData: datatype.UTF8String(supi[5:]),
		}
	case "nai":
		subscriberIdentifier = &dataType.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	case "gci":
		subscriberIdentifier = &dataType.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	case "gli":
		subscriberIdentifier = &dataType.SubscriptionId{
			SubscriptionIdType: dataType.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	}

	for _, unitUsage := range chargingData.MultipleUnitUsage {
		var totalUsaedUnit uint32
		var requestSubType rate_datatype.RequestSubType
		var sur *rate_datatype.ServiceUsageRequest
		var finalUnitIndication models.FinalUnitIndication

		offline := true

		for _, useduint := range unitUsage.UsedUnitContainer {
			switch useduint.QuotaManagementIndicator {
			case models.QuotaManagementIndicator_OFFLINE_CHARGING:
				continue
			case models.QuotaManagementIndicator_ONLINE_CHARGING:
				offline = false
				for _, trigger := range chargingData.Triggers {
					// Check if partial record is needed
					if trigger.TriggerType == models.TriggerType_VOLUME_LIMIT &&
						trigger.TriggerCategory == models.TriggerCategory_IMMEDIATE_REPORT {
						partialRecord = true
					}
					if trigger.TriggerType == models.TriggerType_FINAL {
						requestSubType = dataType.REQ_SUBTYPE_DEBIT
					}
				}
				// calculate total used unit
				totalUsaedUnit += uint32(useduint.TotalVolume)
				// For debug
				ue.AccumulateUsage.TotalVolume += int32(totalUsaedUnit)
			}
		}
		if offline {
			continue
		}

		sessionid, err := self.RatingSessionGenerator.Allocate()
		if err != nil {
			logger.ChargingdataPostLog.Errorf("Rating Session Allocate err: %+v", err)
		}

		rg := unitUsage.RatingGroup
		if !ue.FindRatingGroup(rg) {
			ue.RatingGroups = append(ue.RatingGroups, rg)
		}

		// Retrieve quota into ABMF
		filter := bson.M{"ueId": chargingData.SubscriberIdentifier, "ratingGroup": rg}
		chargingInterface, err := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)
		if err != nil {
			logger.ChargingdataPostLog.Errorf("Get quota error: %+v", err)
		}

		quotaStr := chargingInterface["quota"].(string)
		quota, _ := strconv.ParseInt(quotaStr, 10, 64)

		// No quota left, perform final price calculation
		if quota <= 0 {
			requestSubType = dataType.REQ_SUBTYPE_DEBIT
			sur = &rate_datatype.ServiceUsageRequest{
				SessionId:      datatype.UTF8String(strconv.Itoa(int(sessionid))),
				OriginHost:     datatype.DiameterIdentity(self.RatingCfg.OriginHost),
				OriginRealm:    datatype.DiameterIdentity(self.RatingCfg.OriginRealm),
				ActualTime:     datatype.Time(time.Now()),
				SubscriptionId: subscriberIdentifier,
				UserName:       datatype.OctetString(self.Name),
				ServiceRating: &rate_datatype.ServiceRating{
					ServiceIdentifier: datatype.Unsigned32(rg),
					ConsumedUnits:     datatype.Unsigned32(totalUsaedUnit),
					RequestSubType:    requestSubType,
				},
			}
		} else {
			// reserve account balance
			requestSubType = rate_datatype.REQ_SUBTYPE_RESERVE
			logger.ChargingdataPostLog.Tracef("MonetaryQuota Quota before reserved: %d", quota)

			if ue.ReservedQuota[rg] == 0 {
				ue.ReservedQuota[rg] = int64(unitUsage.RequestedUnit.TotalVolume * 10)
				if ue.ReservedQuota[rg] > quota {
					logger.ChargingdataPostLog.Tracef("Last granted quota")
					finalUnitIndication = models.FinalUnitIndication{
						FinalUnitAction: models.FinalUnitAction_TERMINATE,
					}
					ue.ReservedQuota[rg] = quota
				}
				quota -= ue.ReservedQuota[rg]
				logger.ChargingdataPostLog.Tracef("First quota reserved: %d", ue.ReservedQuota[rg])
			} else {
				usedQuota := totalUsaedUnit * ue.UnitCost[rg]
				insufficient := int64(usedQuota) - ue.ReservedQuota[rg]
				logger.ChargingdataPostLog.Tracef("insufficient: %v, used quota: %v", insufficient, usedQuota)

				if insufficient > 0 {
					// Before reserve, first deduct the insufficient from the quota
					quota -= insufficient
					// make sure that the next reserved quota is bigger then the next used quota
					ue.ReservedQuota[rg] += insufficient * 3
					if ue.ReservedQuota[rg] > quota {
						logger.ChargingdataPostLog.Errorf("Last granted quota")
						finalUnitIndication = models.FinalUnitIndication{
							FinalUnitAction: models.FinalUnitAction_TERMINATE,
						}
						ue.ReservedQuota[rg] = quota
					}
					logger.ChargingdataPostLog.Tracef("Reserved: %v", ue.ReservedQuota[rg])
					quota -= ue.ReservedQuota[rg]
				} else {
					// If the reserved quota is bigger then the used quota,
					// replenish the used quota to the ReservedQuota
					// so that ReservedQuota remains the same
					logger.ChargingdataPostLog.Tracef("still remain reserved quota , replenish: %v", usedQuota)
					quota -= int64(usedQuota)

				}
			}

			// Update quota into ABMF after reserved
			chargingBsonM := make(bson.M)
			chargingBsonM["quota"] = strconv.FormatInt(quota, 10)
			if _, err := mongoapi.RestfulAPIPutOne(chargingDataColl, filter, chargingBsonM); err != nil {
				logger.ChargingdataPostLog.Errorf("RestfulAPIPutOne err: %+v", err)
			}
			logger.ChargingdataPostLog.Infof("UE's [%s] MonetaryQuota After reserved: [%v]", supi, quota)

			// retrived tarrif
			sur = &rate_datatype.ServiceUsageRequest{
				SessionId:      datatype.UTF8String(strconv.Itoa(int(sessionid))),
				OriginHost:     datatype.DiameterIdentity(self.RatingCfg.OriginHost),
				OriginRealm:    datatype.DiameterIdentity(self.RatingCfg.OriginRealm),
				ActualTime:     datatype.Time(time.Now()),
				SubscriptionId: subscriberIdentifier,
				UserName:       datatype.OctetString(self.Name),
				ServiceRating: &rate_datatype.ServiceRating{
					ServiceIdentifier: datatype.Unsigned32(rg),
					MonetaryQuota:     datatype.Unsigned32(ue.ReservedQuota[rg]),
					RequestSubType:    requestSubType,
				},
			}
		}

		serviceUsageRsp, err := rating.SendServiceUsageRequest(ue, sur)
		if err != nil {
			logger.ChargingdataPostLog.Errorf("SendServiceUsageRequest err: %+v", err)
			continue
		}

		// Save the tariff for pricing the next usage
		ue.UnitCost[rg] = uint32(serviceUsageRsp.ServiceRating.MonetaryTariff.RateElement.UnitCost.ValueDigits) *
			uint32(math.Pow10(int(serviceUsageRsp.ServiceRating.MonetaryTariff.RateElement.UnitCost.Exponent)))
		logger.ChargingdataPostLog.Tracef("unitcost for the next usage: %d", ue.UnitCost[rg])

		unitInformation := models.MultipleUnitInformation{
			UPFID:               unitUsage.UPFID,
			FinalUnitIndication: &finalUnitIndication,
			RatingGroup:         rg,
		}

		switch requestSubType {
		case dataType.REQ_SUBTYPE_RESERVE:
			grantedUnit := uint32(serviceUsageRsp.ServiceRating.AllowedUnits)
			unitInformation.VolumeQuotaThreshold = int32(float32(grantedUnit) * ue.VolumeThresholdRate)
			unitInformation.GrantedUnit = &models.GrantedUnit{
				TotalVolume:    int32(grantedUnit),
				DownlinkVolume: int32(grantedUnit),
				UplinkVolume:   int32(grantedUnit),
			}

			if ue.VolumeLimit != 0 {
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Trigger{
						TriggerType:     models.TriggerType_VOLUME_LIMIT,
						TriggerCategory: models.TriggerCategory_DEFERRED_REPORT,
						VolumeLimit:     ue.VolumeLimit,
					},
				)
			}

			if ue.VolumeLimitPDU != 0 && !addPDULimit {
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Trigger{
						TriggerType:     models.TriggerType_VOLUME_LIMIT,
						TriggerCategory: models.TriggerCategory_IMMEDIATE_REPORT,
						VolumeLimit:     ue.VolumeLimitPDU,
					},
				)
				addPDULimit = true
			}

			if ue.QuotaValidityTime != 0 {
				unitInformation.ValidityTime = ue.QuotaValidityTime
			}
		case dataType.REQ_SUBTYPE_DEBIT:
			// final account control
			logger.ChargingdataPostLog.Warnf("Debit mode, will not further grant unit")
			quota -= int64(sur.ServiceRating.Price)
			chargingBsonM := make(bson.M)
			chargingBsonM["quota"] = strconv.FormatInt(quota, 10)
			if _, err := mongoapi.RestfulAPIPutOne(chargingDataColl, filter, chargingBsonM); err != nil {
				logger.ChargingdataPostLog.Errorf("RestfulAPIPutOne err: %+v", err)
			}
			logger.ChargingdataPostLog.Infof("UE's [%s] MonetaryQuota: [%d]", supi, quota)
		}

		multipleUnitInformation = append(multipleUnitInformation, unitInformation)
	}

	return multipleUnitInformation, partialRecord
}
