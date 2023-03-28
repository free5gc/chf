package producer

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/free5gc/CDRUtil/cdrType"
	"github.com/free5gc/TarrifUtil/tarrifType"
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
	responseBody, partialRecord := BuildOnlineChargingDataUpdateResopone(ue, chargingData)

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

	multipleUnitInformation, _ := allocateQuota(ue, chargingData)

	responseBody := models.ChargingDataResponse{
		MultipleUnitInformation: multipleUnitInformation,
	}

	return responseBody
}

func BuildOnlineChargingDataUpdateResopone(ue *chf_context.ChfUe, chargingData models.ChargingDataRequest) (models.ChargingDataResponse, bool) {
	var partialRecord bool

	logger.ChargingdataPostLog.Info("In BuildOnlineChargingDataUpdateResopone ")

	multipleUnitInformation, partialRecord := allocateQuota(ue, chargingData)

	responseBody := models.ChargingDataResponse{
		MultipleUnitInformation: multipleUnitInformation,
	}

	return responseBody, partialRecord
}

func allocateQuota(ue *chf_context.ChfUe, chargingData models.ChargingDataRequest) ([]models.MultipleUnitInformation, bool) {
	var multipleUnitInformation []models.MultipleUnitInformation
	var addPDULimit bool
	var partialRecord bool

	supi := chargingData.SubscriberIdentifier

	for _, unitUsage := range chargingData.MultipleUnitUsage {
		ratingGroup := unitUsage.RatingGroup
		if !ue.FindRatingGroup(ratingGroup) {
			ue.RatingGroups = append(ue.RatingGroups, ratingGroup)
		}

		ServiceUsageRequest := rating.BuildServiceUsageRequest(chargingData, unitUsage)
		logger.ChargingdataPostLog.Tracef("Rate for UE's[%s] rating group[%d]", supi, ratingGroup)
		rsp, _, lastgrantedquota := rating.ServiceUsageRetrieval(ServiceUsageRequest)

		unitInformation := models.MultipleUnitInformation{
			UPFID:               unitUsage.UPFID,
			RatingGroup:         ratingGroup,
			FinalUnitIndication: &models.FinalUnitIndication{},
			// TODO: Control by Webconsole or Config?
			ValidityTime: ue.QuotaValidityTime,
		}

		for _, uuc := range unitUsage.UsedUnitContainer {
			for _, t := range uuc.Triggers {
				if t.TriggerType == models.TriggerType_VOLUME_LIMIT &&
					t.TriggerCategory == models.TriggerCategory_IMMEDIATE_REPORT {
					partialRecord = true
				}
			}
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
		if ServiceUsageRequest.ServiceRating.RequestSubType.Value == tarrifType.REQ_SUBTYPE_RESERVE && rsp.ServiceRating.AllowedUnits != 0 {
			unitInformation.VolumeQuotaThreshold = int32(float32(rsp.ServiceRating.AllowedUnits) * ue.VolumeThresholdRate)
			unitInformation.GrantedUnit = &models.GrantedUnit{
				TotalVolume:    int32(rsp.ServiceRating.AllowedUnits),
				DownlinkVolume: int32(rsp.ServiceRating.AllowedUnits),
				UplinkVolume:   int32(rsp.ServiceRating.AllowedUnits),
			}
		}

		if lastgrantedquota {
			unitInformation.FinalUnitIndication = &models.FinalUnitIndication{
				FinalUnitAction: models.FinalUnitAction_TERMINATE,
			}
			logger.ChargingdataPostLog.Infof("UE's [%s] last granted quota: %d", supi, rsp.ServiceRating.AllowedUnits)
		}

		remainQuota := int32(rsp.ServiceRating.MonetaryQuota) - int32(rsp.ServiceRating.Price)

		filter := bson.M{"ueId": chargingData.SubscriberIdentifier, "ratingGroup": 1}
		chargingBsonM, err := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)
		if err != nil {
			logger.ChargingdataPostLog.Errorf("RestfulAPIGetOne err: %+v", err)
		}

		if remainQuota < 0 {
			chargingBsonM["quota"] = uint32(0)
			// chargingBsonM["debit"] = remainQuota
		} else {
			chargingBsonM["quota"] = uint32(remainQuota)
		}

		if err := mongoapi.RestfulAPIDeleteMany(chargingDataColl, filter); err != nil {
			logger.ChargingdataPostLog.Errorf("RestfulAPIDeleteMany err: %+v", err)
		}

		if _, err := mongoapi.RestfulAPIPutOne(chargingDataColl, filter, chargingBsonM); err != nil {
			logger.ChargingdataPostLog.Errorf("RestfulAPIPutOne err: %+v", err)
		}
		logger.ChargingdataPostLog.Infof("UE's [%s] MonetaryQuota: [%d]", supi, remainQuota)

		multipleUnitInformation = append(multipleUnitInformation, unitInformation)
	}

	return multipleUnitInformation, partialRecord
}
