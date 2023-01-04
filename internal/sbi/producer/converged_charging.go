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

func NotifyRecharge(ratingGroup int32) {
	self := chf_context.CHF_Self()

	//TODO: send notify to all UE's rating group
	notifyRequest := models.ChargingNotifyRequest{
		ReauthorizationDetails: []models.ReauthorizationDetails{
			{RatingGroup: ratingGroup},
		},
	}

	SendChargingNotification(self.NotifyUri, notifyRequest)
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

	quotaManagementIndicator := chargingData.MultipleUnitUsage[0].UsedUnitContainer[0].QuotaManagementIndicator
	if quotaManagementIndicator == models.QuotaManagementIndicator_ONLINE_CHARGING {
		responseBody = BuildOnlineChargingDataCreateResopone(chargingData)
	}

	// Open CDR
	// ChargingDataRef(charging session id):
	// A unique identifier for a charging data resource in a PLMN
	// TODO determine charging session id(string type) supi+consumerid+localseq?
	ueId := chargingData.SubscriberIdentifier
	self.UeIdRatingGroupMap[ueId] = chargingData.MultipleUnitUsage[0].RatingGroup
	consumerId := chargingData.NfConsumerIdentification.NFName

	if !chargingData.OneTimeEvent {
		chargingSessionId = ueId + consumerId + strconv.Itoa(int(self.LocalRecordSequenceNumber))
	}
	cdr, err := OpenCDR(chargingData, ueId, chargingSessionId, false)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}
	self.ChargingSession[chargingSessionId] = cdr

	err = UpdateCDR(cdr, chargingData, chargingSessionId, false)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}

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
	_, err = self.NewCHFUe(ueId)

	if err != nil {
		logger.ChargingdataPostLog.Errorf("New CHFUe error %s", err)
	}
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
	var responseBody models.ChargingDataResponse
	var partialRecord bool

	self := chf_context.CHF_Self()

	// Online charging: Rate, Account, Reservation
	quotaManagementIndicator := chargingData.MultipleUnitUsage[0].UsedUnitContainer[0].QuotaManagementIndicator
	if quotaManagementIndicator == models.QuotaManagementIndicator_ONLINE_CHARGING {
		responseBody, partialRecord = BuildOnlineChargingDataUpdateResopone(chargingData, chargingSessionId)
	}

	cdr := self.ChargingSession[chargingSessionId]
	err := UpdateCDR(cdr, chargingData, chargingSessionId, false)
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

		OpenCDR(chargingData, ueId, chargingSessionId, partialRecord)
		logger.ChargingdataPostLog.Info("CDR: %+v", self.ChargingSession[chargingSessionId].ChargingFunctionRecord)
	}
	// NOTE: for demo
	ueId := chargingData.SubscriberIdentifier
	err = dumpCdrFile(ueId, []*cdrType.CHFRecord{cdr})
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
	cdr := self.ChargingSession[chargingSessionId]

	err := UpdateCDR(cdr, chargingData, chargingSessionId, false)
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

	ueId := chargingData.SubscriberIdentifier
	err = dumpCdrFile(ueId, []*cdrType.CHFRecord{cdr})
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return problemDetails
	}

	return nil
}

func BuildOnlineChargingDataCreateResopone(chargingData models.ChargingDataRequest) models.ChargingDataResponse {
	logger.ChargingdataPostLog.Info("In BuildOnlineChargingDataCreateResopone ")
	self := chf_context.CHF_Self()

	self.NotifyUri = chargingData.NotifyUri
	multipleUnitInformation := []models.MultipleUnitInformation{}
	supi := chargingData.SubscriberIdentifier

	for _, unitUsage := range chargingData.MultipleUnitUsage {
		ratingGroup := unitUsage.RatingGroup
		if sessionid, err := self.RatingSessionGenerator.Allocate(); err == nil {
			ServiceUsageRequest := rating.BuildServiceUsageRequest(chargingData, unitUsage, sessionid, ratingGroup)
			rsp, _, lastgrantedquota := rating.ServiceUsageRetrieval(ServiceUsageRequest)

			unitInformation := models.MultipleUnitInformation{
				RatingGroup:          ratingGroup,
				VolumeQuotaThreshold: int32(float32(rsp.ServiceRating.AllowedUnits) * 0.8),
				Triggers: []models.Trigger{
					{
						TriggerType: models.TriggerType_VOLUME_LIMIT,
						VolumeLimit: self.VolumeLimit,
					},
				},
				FinalUnitIndication: &models.FinalUnitIndication{},
				GrantedUnit: &models.GrantedUnit{
					TotalVolume:    int32(rsp.ServiceRating.AllowedUnits),
					DownlinkVolume: int32(rsp.ServiceRating.AllowedUnits),
					UplinkVolume:   int32(rsp.ServiceRating.AllowedUnits),
				},
				// TODO: Control by Webconsole or Config?
				ValidityTime: self.QuotaValidityTime,
			}

			if lastgrantedquota {
				unitInformation.FinalUnitIndication = &models.FinalUnitIndication{
					FinalUnitAction: models.FinalUnitAction_TERMINATE,
				}
				logger.ChargingdataPostLog.Infof("Last granted unit for UE [%s]: [%d]", supi, rsp.ServiceRating.AllowedUnits)
			}

			quota := ServiceUsageRequest.ServiceRating.MonetaryQuota

			logger.ChargingdataPostLog.Infof("UE's [%s] MonetaryQuota: [%d]", supi, quota)
			multipleUnitInformation = append(multipleUnitInformation, unitInformation)
		}
	}
	responseBody := models.ChargingDataResponse{}
	responseBody.MultipleUnitInformation = multipleUnitInformation

	return responseBody
}

func BuildOnlineChargingDataUpdateResopone(chargingData models.ChargingDataRequest, chargingSessionId string) (models.ChargingDataResponse, bool) {
	var partialRecord bool

	logger.ChargingdataPostLog.Info("In BuildOnlineChargingDataUpdateResopone ")
	self := chf_context.CHF_Self()
	supi := chargingData.SubscriberIdentifier
	multipleUnitInformation := []models.MultipleUnitInformation{}
	// Rating for each report
	for _, trigger := range chargingData.Triggers {
		if trigger.TriggerType == models.TriggerType_VALIDITY_TIME {
			partialRecord = true
		}
	}
	for _, unitUsage := range chargingData.MultipleUnitUsage {
		ratingGroup := unitUsage.RatingGroup
		if sessionid, err := self.RatingSessionGenerator.Allocate(); err == nil {
			ServiceUsageRequest := rating.BuildServiceUsageRequest(chargingData, unitUsage, sessionid, ratingGroup)
			rsp, _, lastgrantedquota := rating.ServiceUsageRetrieval(ServiceUsageRequest)

			unitInformation := models.MultipleUnitInformation{
				RatingGroup: ratingGroup,
				Triggers: []models.Trigger{
					{
						TriggerType: models.TriggerType_VOLUME_LIMIT,
						VolumeLimit: self.VolumeLimit,
					},
				},
				FinalUnitIndication: &models.FinalUnitIndication{},
				// TODO: Control by Webconsole or Config?
				ValidityTime: self.QuotaValidityTime,
			}

			if ServiceUsageRequest.ServiceRating.RequestSubType.Value == tarrifType.REQ_SUBTYPE_RESERVE && rsp.ServiceRating.AllowedUnits != 0 {
				unitInformation.VolumeQuotaThreshold = int32(float32(rsp.ServiceRating.AllowedUnits) * 0.8)
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
			chargingBsonM := bson.M{}
			if chargingBsonM, err = mongoapi.RestfulAPIGetOne(chargingDataColl, filter); err != nil {
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
		} else {
			logger.ChargingdataPostLog.Errorf("Rating Session Allocate err: %+v", err)
		}
	}

	responseBody := models.ChargingDataResponse{}
	responseBody.MultipleUnitInformation = multipleUnitInformation

	return responseBody, partialRecord
}
