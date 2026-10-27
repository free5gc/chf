package processor

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/exp/constraints"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/cgf"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/util"
	Nchf_ConvergedCharging "github.com/free5gc/openapi/chf/ConvCharging"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/metrics/sbi"
)

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func (p *Processor) NotifyRecharge(ueId string, rg int32) {
	var reauthorizationDetails []models.Chf_ConvCharging_ReauthorizationDetails

	self := chf_context.GetSelf()
	ue, ok := self.ChfUeFindBySupi(ueId)
	if !ok {
		logger.NotifyEventLog.Errorf("Do not find charging data for UE: %s", ueId)
		return
	}

	ue.CULock.Lock()
	active, rechargeErr := p.rechargeQuotas(ue, rg)
	notifyURI := ue.NotifyUri
	ue.CULock.Unlock()
	if rechargeErr != nil {
		logger.NotifyEventLog.Errorf("Queue quota recharge: %v", rechargeErr)
		return
	}
	if !active {
		return
	}
	reauthorizationDetails = append(reauthorizationDetails, models.Chf_ConvCharging_ReauthorizationDetails{
		RatingGroup: rg,
	})

	notifyRequest := models.Chf_ConvCharging_ChargingNotifyRequest{
		NotificationType:       models.Chf_ConvCharging_NotificationType_REAUTHORIZATION,
		ReauthorizationDetails: reauthorizationDetails,
	}

	p.SendChargingNotification(notifyURI, notifyRequest)
}

func (p *Processor) SendChargingNotification(
	notifyUri string, notifyRequest models.Chf_ConvCharging_ChargingNotifyRequest,
) {
	client := util.GetNchfChargingNotificationCallbackClient()
	logger.NotifyEventLog.Warn("Send Charging Notification  to SMF: uri: ", notifyUri)
	chargingNotifyRequest := &Nchf_ConvergedCharging.NullChargingNotificationRequest{
		RequestBody: &notifyRequest,
	}

	ctx, pd, err := chf_context.GetSelf().GetTokenCtx(
		models.Nrf_NFMgmt_ServiceName("nsmf-callback"), models.Nrf_NFMgmt_NFType_SMF)
	if err != nil {
		logger.NotifyEventLog.Warnf("SendChargingNotification get token failed: %+v", pd)
		return
	}

	_, err = client.DefaultApi.NullChargingNotification(ctx, notifyUri, chargingNotifyRequest)
	if err != nil {
		logger.NotifyEventLog.Warnf("Charging Notification Failed[%s]", err.Error())
		return
	}

	logger.NotifyEventLog.Tracef("Charging Notification Success")
}

func (p *Processor) HandleChargingdataInitial(
	c *gin.Context,
	chargingdata models.Chf_ConvCharging_ChargingDataRequest,
) {
	logger.ChargingdataPostLog.Infof("HandleChargingdataInitial")

	response, locationURI, problemDetails := p.ChargingDataCreate(chargingdata)

	if response != nil {
		c.Header("Location", locationURI)
		c.JSON(http.StatusCreated, response)
		return
	} else if problemDetails != nil {
		c.Set(sbi.IN_PB_DETAILS_CTX_STR, problemDetails.Cause)
		c.JSON(int(problemDetails.Status), problemDetails)
		return
	}
	problemDetails = &models.ProblemDetails{
		Status: http.StatusForbidden,
		Cause:  "UNSPECIFIED",
	}
	c.Set(sbi.IN_PB_DETAILS_CTX_STR, problemDetails.Cause)
	c.JSON(int(problemDetails.Status), problemDetails)
}

func (p *Processor) HandleChargingdataUpdate(
	c *gin.Context,
	chargingdata models.Chf_ConvCharging_ChargingDataRequest,
	chargingSessionId string,
) {
	logger.ChargingdataPostLog.Infof("HandleChargingdataUpdate")

	response, problemDetails := p.ChargingDataUpdate(chargingdata, chargingSessionId)

	if response != nil {
		c.JSON(http.StatusOK, response)
		return
	} else if problemDetails != nil {
		c.Set(sbi.IN_PB_DETAILS_CTX_STR, problemDetails.Cause)
		c.JSON(int(problemDetails.Status), problemDetails)
		return
	}
	problemDetails = &models.ProblemDetails{
		Status: http.StatusForbidden,
		Cause:  "UNSPECIFIED",
	}
	c.Set(sbi.IN_PB_DETAILS_CTX_STR, problemDetails.Cause)
	c.JSON(int(problemDetails.Status), problemDetails)
}

func (p *Processor) HandleChargingdataRelease(
	c *gin.Context,
	chargingdata models.Chf_ConvCharging_ChargingDataRequest,
	chargingSessionId string,
) {
	logger.ChargingdataPostLog.Infof("HandleChargingdateRelease")

	problemDetails := p.ChargingDataRelease(chargingdata, chargingSessionId)
	if problemDetails == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.Set(sbi.IN_PB_DETAILS_CTX_STR, problemDetails.Cause)
	c.JSON(int(problemDetails.Status), problemDetails)
}

func (p *Processor) ChargingDataCreate(
	chargingData models.Chf_ConvCharging_ChargingDataRequest,
) (
	*models.Chf_ConvCharging_ChargingDataResponse,
	string, *models.ProblemDetails,
) {
	var responseBody models.Chf_ConvCharging_ChargingDataResponse
	var chargingSessionId string

	if problemDetails := util.ValidateChargingDataCreateRequest(chargingData); problemDetails != nil {
		return nil, "", problemDetails
	}

	self := chf_context.GetSelf()
	ueId := chargingData.SubscriberIdentifier

	// Open CDR
	// ChargingDataRef(charging session id):
	// A unique identifier for a charging data resource in a PLMN
	// Include a UUID so restarting CHF cannot reuse a persisted resource identity.
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
	chargingSessionId = ueId + "-" + consumerId + "-" + uuid.NewString()
	cdr, err := p.OpenCDR(chargingData, ue, chargingSessionId, false)
	if err != nil {
		logger.ChargingdataPostLog.Errorf("OpenCDR failed: %v", err)
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}

	err = p.UpdateCDR(cdr, chargingData)
	if err != nil {
		problemDetails := &models.ProblemDetails{
			Status: http.StatusBadRequest,
		}
		return nil, "", problemDetails
	}

	if chargingData.OneTimeEvent {
		err = p.CloseCDR(cdr, false)
		if err != nil {
			problemDetails := &models.ProblemDetails{
				Status: http.StatusBadRequest,
			}
			return nil, "", problemDetails
		}
	}

	if p.journal == nil {
		return nil, "", chargingProblem(fmt.Errorf("charging journal is unavailable"))
	}
	ctx, cancel := journalContext()
	err = p.journal.InitializeCDR(ctx, chargingSessionId, &charging.CDRState{
		Subscriber: ueId, NotifyURI: ue.NotifyUri, Current: cdr, Closed: chargingData.OneTimeEvent,
	})
	cancel()
	if err != nil {
		return nil, "", chargingProblem(err)
	}
	ue.Cdr[chargingSessionId] = cdr
	if ue.CDRProgress == nil {
		ue.CDRProgress = make(map[string]charging.CDRProgress)
	}
	ue.CDRProgress[chargingSessionId] = charging.CDRProgress{Closed: chargingData.OneTimeEvent}
	if err = mergeCDRRecords(ue, []*cdrType.CHFRecord{cdr}); err != nil {
		return nil, "", chargingProblem(err)
	}
	if err = p.writeCDRs(ueId, ue.Records); err != nil {
		return nil, "", chargingProblem(err)
	}

	// CDR Transfer
	err = cgf.SendCDR(chargingData.SubscriberIdentifier)
	if err != nil {
		logger.ChargingdataPostLog.Errorf("Charging gateway fail to send CDR to billing domain %v", err)
	}

	logger.ChargingdataPostLog.Infof("Open CDR for UE %s", ueId)

	// build response
	logger.ChargingdataPostLog.Infof("NewChfUe %s", ueId)
	locationURI := self.Url + "/nchf-convergedcharging/v3/chargingdata/" + chargingSessionId
	timeStamp := time.Now()

	responseBody.InvocationTimeStamp = &timeStamp
	responseBody.InvocationSequenceNumber = chargingData.InvocationSequenceNumber

	return &responseBody, locationURI, nil
}

func (p *Processor) ChargingDataUpdate(
	chargingData models.Chf_ConvCharging_ChargingDataRequest, chargingSessionId string,
) (*models.Chf_ConvCharging_ChargingDataResponse, *models.ProblemDetails) {
	record, problem := p.beginChargingRequest(chargingData, chargingSessionId, chargingUpdateOperation)
	if problem != nil {
		return nil, problem
	}
	// Only a replay needs decoding; a fresh request returns the response built below.
	if record.State == charging.Succeeded {
		var response models.Chf_ConvCharging_ChargingDataResponse
		if err := json.Unmarshal(record.Result.Body, &response); err != nil {
			return nil, chargingProblem(err)
		}
		return &response, nil
	}

	stopLease := p.keepChargingLease(record)
	defer stopLease()
	ue, problem := p.findChargingUE(chargingData.SubscriberIdentifier, record)
	if problem != nil {
		return nil, problem
	}

	ue.CULock.Lock()
	defer ue.CULock.Unlock()

	if problem = p.restoreChargingResource(ue, record); problem != nil {
		return nil, problem
	}

	if err := p.restoreChargingState(ue, record); err != nil {
		return nil, p.failChargingRequest(record, chargingProblem(err))
	}
	if ue.CDRProgress[chargingSessionId].Closed {
		return nil, p.rejectChargingRequest(record, missingChargingSession(chargingSessionId))
	}
	if ownershipProblem := validateQuotaOwners(ue, chargingData, chargingSessionId); ownershipProblem != nil {
		return nil, p.rejectChargingRequest(record, ownershipProblem)
	}

	// Online charging: Rate, Account, Reservation
	responseBody, partialRecord, reservationErr := p.BuildConvergedChargingDataUpdateResopone(
		chargingData, chargingSessionId, record)
	if reservationErr != nil {
		return nil, p.failChargingRequest(record, chargingProblem(reservationErr))
	}

	if err := p.applyChargingCDR(ue, record, partialRecord); err != nil {
		return nil, p.failChargingRequest(record, chargingProblem(err))
	}

	err := cgf.SendCDR(chargingData.SubscriberIdentifier)
	if err != nil {
		logger.ChargingdataPostLog.Errorf("Charging gateway fail to send CDR to billing domain %v", err)
	}

	timeStamp := time.Now()
	responseBody.InvocationTimeStamp = &timeStamp
	responseBody.InvocationSequenceNumber = chargingData.InvocationSequenceNumber

	body, encodeErr := json.Marshal(responseBody)
	if encodeErr != nil {
		return nil, p.failChargingRequest(record, chargingProblem(encodeErr))
	}
	if problem = p.finishChargingRequest(record, charging.Result{Status: http.StatusOK, Body: body},
		charging.Succeeded); problem != nil {
		return nil, problem
	}
	return &responseBody, nil
}

func (p *Processor) ChargingDataRelease(
	chargingData models.Chf_ConvCharging_ChargingDataRequest, chargingSessionId string,
) *models.ProblemDetails {
	record, problem := p.beginChargingRequest(chargingData, chargingSessionId, chargingReleaseOperation)
	if problem != nil {
		return problem
	}
	if record.State == charging.Succeeded {
		return nil
	}

	stopLease := p.keepChargingLease(record)
	defer stopLease()
	ue, problem := p.findChargingUE(chargingData.SubscriberIdentifier, record)
	if problem != nil {
		return problem
	}

	ue.CULock.Lock()
	defer ue.CULock.Unlock()

	if problem = p.restoreChargingResource(ue, record); problem != nil {
		return problem
	}
	if err := p.restoreChargingState(ue, record); err != nil {
		return p.failChargingRequest(record, chargingProblem(err))
	}
	if ue.CDRProgress[chargingSessionId].Closed {
		return p.rejectChargingRequest(record, missingChargingSession(chargingSessionId))
	}
	if ownershipProblem := validateQuotaOwners(ue, chargingData, chargingSessionId); ownershipProblem != nil {
		return p.rejectChargingRequest(record, ownershipProblem)
	}
	if _, _, err := p.sessionChargingReservation(chargingData, chargingSessionId, record); err != nil {
		return p.failChargingRequest(record, chargingProblem(err))
	}

	if err := p.applyChargingCDR(ue, record, false); err != nil {
		return p.failChargingRequest(record, chargingProblem(err))
	}

	return p.finishChargingRequest(record, charging.Result{Status: http.StatusNoContent}, charging.Succeeded)
}

func (p *Processor) BuildOnlineChargingDataCreateResopone(
	ue *chf_context.ChfUe, chargingData models.Chf_ConvCharging_ChargingDataRequest, chargingSessionId string,
) (models.Chf_ConvCharging_ChargingDataResponse, error) {
	logger.ChargingdataPostLog.Info("In Build Online Charging Data Create Resopone")
	ue.NotifyUri = chargingData.NotifyUri

	multipleUnitInformation, _, err := p.sessionChargingReservation(chargingData, chargingSessionId, nil)

	responseBody := models.Chf_ConvCharging_ChargingDataResponse{
		MultipleUnitInformation: multipleUnitInformation,
	}

	return responseBody, err
}

func (p *Processor) BuildConvergedChargingDataUpdateResopone(
	chargingData models.Chf_ConvCharging_ChargingDataRequest, chargingSessionId string, record *charging.Record,
) (models.Chf_ConvCharging_ChargingDataResponse, bool, error) {
	logger.ChargingdataPostLog.Info("In BuildConvergedChargingDataUpdateResopone")

	multipleUnitInformation, partialRecord, err := p.sessionChargingReservation(chargingData, chargingSessionId, record)

	responseBody := models.Chf_ConvCharging_ChargingDataResponse{
		MultipleUnitInformation: multipleUnitInformation,
	}

	return responseBody, partialRecord, err
}

func (p *Processor) getUnitCost(
	ue *chf_context.ChfUe, rg int32, sur *charging_datatype.ServiceUsageRequest,
	ratings *[]charging.RatingExchange,
) (uint32, error) {
	if sur == nil {
		return 0, fmt.Errorf("service usage request is nil")
	}
	sur.ServiceRating = &charging_datatype.ServiceRating{
		ServiceIdentifier: datatype.Unsigned32(rg),
		MonetaryQuota:     0,
		RequestSubType:    charging_datatype.REQ_SUBTYPE_RESERVE,
	}
	response, err := p.captureServiceUsage(ue, sur, ratings)
	if err != nil {
		return 0, err
	}
	if response == nil || response.ServiceRating == nil || response.ServiceRating.MonetaryTariff == nil ||
		response.ServiceRating.MonetaryTariff.RateElement == nil ||
		response.ServiceRating.MonetaryTariff.RateElement.UnitCost == nil {
		return 0, fmt.Errorf("rating response is missing unit cost")
	}
	cost := response.ServiceRating.MonetaryTariff.RateElement.UnitCost
	return uint32(cost.ValueDigits) * uint32(math.Pow10(int(cost.Exponent))), nil
}

// 32.296 6.2.2.3.1: Service usage request method with reservation
func (p *Processor) sessionChargingReservation(
	chargingData models.Chf_ConvCharging_ChargingDataRequest, chargingSessionId string, record *charging.Record,
) ([]models.Chf_ConvCharging_MultipleUnitInformation, bool, error) {
	var multipleUnitInformation []models.Chf_ConvCharging_MultipleUnitInformation
	var partialRecord bool
	var subscriberIdentifier *charging_datatype.SubscriptionId

	self := chf_context.GetSelf()
	supi := chargingData.SubscriberIdentifier

	ue, ok := self.ChfUeFindBySupi(supi)
	if !ok {
		logger.ChargingdataPostLog.Warnf("Do not find UE[%s]", supi)
		return nil, false, fmt.Errorf("charging UE %s not found", supi)
	}

	supiType := strings.Split(supi, "-")[0]
	switch supiType {
	case "imsi":
		subscriberIdentifier = &charging_datatype.SubscriptionId{
			SubscriptionIdType: charging_datatype.END_USER_IMSI,
			SubscriptionIdData: datatype.UTF8String(supi[5:]),
		}
	case "nai":
		subscriberIdentifier = &charging_datatype.SubscriptionId{
			SubscriptionIdType: charging_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	case "gci":
		subscriberIdentifier = &charging_datatype.SubscriptionId{
			SubscriptionIdType: charging_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	case "gli":
		subscriberIdentifier = &charging_datatype.SubscriptionId{
			SubscriptionIdType: charging_datatype.END_USER_NAI,
			SubscriptionIdData: datatype.UTF8String(supi[4:]),
		}
	}

	for unitUsageNum, unitUsage := range chargingData.MultipleUnitUsage {
		if record != nil && record.SettlementPlan != nil &&
			len(record.SettlementPlan.Steps[strconv.Itoa(unitUsageNum)]) != 0 {
			info, partial, resumeErr := p.resumeSettlementEntry(ue, record, unitUsageNum)
			if resumeErr != nil {
				return nil, false, resumeErr
			}
			multipleUnitInformation = append(multipleUnitInformation, info)
			partialRecord = partial
			continue
		}
		var totalUsedUnit uint32
		var finalUnitIndication models.Chf_ConvCharging_FinalUnitIndication
		creditControl := false

		rg := unitUsage.RatingGroup
		key := charging.QuotaKey{ChargingDataRef: chargingSessionId, RatingGroup: rg, UPFID: unitUsage.UPFID}
		before, known := ue.Quotas[key]
		before.Known = known
		reservedQuota, ratingType, unitCost := before.Reserved, before.RatingType, before.UnitCost
		exhausted, closed := before.Exhausted, before.Closed
		if !before.Known {
			ratingType = charging_datatype.REQ_SUBTYPE_RESERVE
		}
		var ratings []charging.RatingExchange
		var accountRequest *charging_datatype.AccountDebitRequest
		var addGrantedUnits bool
		thresholdIndex := -1
		var previousThreshold int32

		unitInformation := models.Chf_ConvCharging_MultipleUnitInformation{
			UPFID:               unitUsage.UPFID,
			FinalUnitIndication: &finalUnitIndication,
			RatingGroup:         rg,
		}

		for _, usedUnit := range unitUsage.UsedUnitContainer {
			switch usedUnit.QuotaManagementIndicator {
			case models.Chf_ConvCharging_QuotaManagementIndicator_OFFLINE_CHARGING:
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Chf_ConvCharging_Trigger{
						TriggerType:     models.Chf_ConvCharging_TriggerType_QUOTA_THRESHOLD,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
					},
				)

				unitInformation.VolumeQuotaThreshold = int32(30000000)
				continue
			case models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING:
				creditControl = true

				for _, trigger := range chargingData.Triggers {
					// Check if partial record is needed
					partialRecord = true
					switch t := trigger; {
					case t == models.Chf_ConvCharging_Trigger{
						TriggerType:     models.Chf_ConvCharging_TriggerType_VOLUME_LIMIT,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
					}:
					case t.TriggerType == models.Chf_ConvCharging_TriggerType_MAX_NUMBER_OF_CHANGES_IN_CHARGING_CONDITIONS:
					case t.TriggerType == models.Chf_ConvCharging_TriggerType_MANAGEMENT_INTERVENTION:
					case t.TriggerType == models.Chf_ConvCharging_TriggerType_FINAL:
						partialRecord = false
					}
				}
				// calculate total used unit
				totalUsedUnit += uint32(usedUnit.TotalVolume)
			case models.Chf_ConvCharging_QuotaManagementIndicator_QUOTA_MANAGEMENT_SUSPENDED:
				logger.ChargingdataPostLog.Errorf("Current do not support QUOTA MANAGEMENT SUSPENDED")
			}
		}
		if !creditControl {
			logger.ChargingdataPostLog.Infof("Credit Control are not required for rating group: %d", rg)
			continue
		}
		// In the independent-UPF model, omitting RequestedUnit ends this owner's
		// quota cycle. Other UPFs using the same RG keep their own reservations.
		if unitUsage.RequestedUnit == nil {
			ratingType = charging_datatype.REQ_SUBTYPE_DEBIT
			closed = true
			partialRecord = false
		} else if unitUsage.RequestedUnit.TotalVolume > 0 {
			// An explicit credit request starts/reuses this UPF's quota cycle.
			// Only a genuine ABMF final grant blocks further credit until recharge.
			if closed {
				exhausted = false
			}
			closed = false
			if !exhausted {
				ratingType = charging_datatype.REQ_SUBTYPE_RESERVE
			}
		}

		ccr := &charging_datatype.AccountDebitRequest{
			CcRequestType:  charging_datatype.UPDATE_REQUEST,
			OriginHost:     datatype.DiameterIdentity(self.AbmfCfg.OriginHost),
			OriginRealm:    datatype.DiameterIdentity(self.AbmfCfg.OriginRealm),
			EventTimestamp: datatype.Time(time.Now().UTC()),
			SubscriptionId: subscriberIdentifier,
			UserName:       datatype.OctetString(self.Name),
		}

		accountID, identityErr := charging.Digest(
			[]any{chargingSessionId, chargingData.InvocationSequenceNumber, unitUsageNum})
		if identityErr != nil {
			return nil, false, identityErr
		}
		ccr.SessionId = datatype.UTF8String("chf;" + accountID)
		ccr.CcRequestNumber = 0

		sur := &charging_datatype.ServiceUsageRequest{
			SessionId:      datatype.UTF8String(strconv.Itoa(int(ue.RateSessionId))),
			OriginHost:     datatype.DiameterIdentity(self.RatingCfg.OriginHost),
			OriginRealm:    datatype.DiameterIdentity(self.RatingCfg.OriginRealm),
			ActualTime:     datatype.Time(time.Now().UTC()),
			SubscriptionId: subscriberIdentifier,
			UserName:       datatype.OctetString(self.Name),
		}

		switch ratingType {
		case charging_datatype.REQ_SUBTYPE_RESERVE:
			// If there is no requested unit, nothing to reserve / grant.
			if unitUsage.RequestedUnit == nil || unitUsage.RequestedUnit.TotalVolume <= 0 {
				logger.ChargingdataPostLog.Infof("No RequestedUnit for rating group: %d, skip granting", rg)
				continue
			}
			var requestedQuota uint64

			cost, costErr := p.getUnitCost(ue, rg, sur, &ratings)
			if costErr != nil {
				return nil, false, costErr
			}
			unitCost = cost

			var requestedTotalVolume uint32
			if unitUsage.RequestedUnit != nil {
				requestedTotalVolume = uint32(unitUsage.RequestedUnit.TotalVolume)
			}

			usedQuota := uint64(totalUsedUnit * unitCost)
			requestedQuota = uint64(requestedTotalVolume * unitCost)
			reservedQuota -= int64(usedQuota)
			NeedReserveQuota := reservedQuota <= 0

			if NeedReserveQuota {
				reserveQuota := -uint64(reservedQuota) + requestedQuota
				ccr.CcRequestType = charging_datatype.UPDATE_REQUEST
				ccr.RequestedAction = charging_datatype.DIRECT_DEBITING
				ccr.MultipleServicesCreditControl = &charging_datatype.MultipleServicesCreditControl{
					RatingGroup: datatype.Unsigned32(rg),
					RequestedServiceUnit: &charging_datatype.RequestedServiceUnit{
						CCTotalOctets: datatype.Unsigned64(reserveQuota),
					},
				}

				accountRequest = ccr
				addGrantedUnits = true
			}

			sur.ServiceRating = &charging_datatype.ServiceRating{
				ServiceIdentifier: datatype.Unsigned32(rg),
				MonetaryQuota:     datatype.Unsigned32(requestedQuota),
				RequestSubType:    charging_datatype.REQ_SUBTYPE_RESERVE,
			}

			// Retrieve and save the tarrif for pricing the next usage
			serviceUsageRsp, err := p.captureServiceUsage(ue, sur, &ratings)
			if err != nil {
				return nil, false, fmt.Errorf("rating operation: %w", err)
			}

			cost, costErr = p.getUnitCost(ue, rg, sur, &ratings)
			if costErr != nil {
				return nil, false, costErr
			}
			unitCost = cost

			if serviceUsageRsp == nil || serviceUsageRsp.ServiceRating == nil {
				return nil, false, fmt.Errorf("rating response is missing service rating")
			}
			grantedUnit := min(uint32(serviceUsageRsp.ServiceRating.AllowedUnits), requestedTotalVolume)

			if ratingType == charging_datatype.REQ_SUBTYPE_RESERVE {
				thresholdIndex = len(unitInformation.Triggers)
				previousThreshold = unitInformation.VolumeQuotaThreshold
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Chf_ConvCharging_Trigger{
						TriggerType:     models.Chf_ConvCharging_TriggerType_QUOTA_THRESHOLD,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
					},
				)

				unitInformation.VolumeQuotaThreshold = int32(float32(grantedUnit) * ue.VolumeThresholdRate)
			}

			unitInformation.Triggers = append(unitInformation.Triggers,
				models.Chf_ConvCharging_Trigger{
					TriggerType:     models.Chf_ConvCharging_TriggerType_QUOTA_EXHAUSTED,
					TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
				},
			)

			unitInformation.GrantedUnit = &models.Chf_ConvCharging_GrantedUnit{
				TotalVolume:    int32(grantedUnit),
				DownlinkVolume: int32(grantedUnit),
				UplinkVolume:   int32(grantedUnit),
			}
			logger.ChargingdataPostLog.Tracef("granted Unit: %d", unitInformation.GrantedUnit.TotalVolume)

			// The timer of VolumeLimit is remain in SMF
			if ue.VolumeLimit != 0 {
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Chf_ConvCharging_Trigger{
						TriggerType:     models.Chf_ConvCharging_TriggerType_VOLUME_LIMIT,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_DEFERRED_REPORT,
						VolumeLimit:     ue.VolumeLimit,
					},
				)
			}

			// VolumeLimit for PDU session only need to add once
			if ue.VolumeLimitPDU != 0 && unitUsageNum == 0 {
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Chf_ConvCharging_Trigger{
						TriggerType:     models.Chf_ConvCharging_TriggerType_VOLUME_LIMIT,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
						VolumeLimit:     ue.VolumeLimitPDU,
					},
				)
			}

			// The timer of QuotaValidityTime is remain in UPF
			if ue.QuotaValidityTime != 0 {
				unitInformation.Triggers = append(unitInformation.Triggers,
					models.Chf_ConvCharging_Trigger{
						TriggerType:     models.Chf_ConvCharging_TriggerType_VALIDITY_TIME,
						TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
					},
				)
				unitInformation.ValidityTime = ue.QuotaValidityTime
			}

		case charging_datatype.REQ_SUBTYPE_DEBIT:
			logger.ChargingdataPostLog.Info("Debit mode, will not grant unit")
			// retrieved tarrif for final pricing
			sur.ServiceRating = &charging_datatype.ServiceRating{
				ServiceIdentifier: datatype.Unsigned32(rg),
				ConsumedUnits:     datatype.Unsigned32(totalUsedUnit),
				RequestSubType:    charging_datatype.REQ_SUBTYPE_DEBIT,
			}

			serviceUsageRsp, err := p.captureServiceUsage(ue, sur, &ratings)
			if err != nil {
				return nil, false, fmt.Errorf("rating operation: %w", err)
			}
			if serviceUsageRsp == nil || serviceUsageRsp.ServiceRating == nil {
				return nil, false, fmt.Errorf("rating response is missing service rating")
			}
			logger.ChargingdataPostLog.Tracef(
				"price %+v, reservedQuota: %+v", serviceUsageRsp.ServiceRating.Price, reservedQuota)

			if int64(serviceUsageRsp.ServiceRating.Price) < reservedQuota {
				// The final consumed quota is smaller than the reserved quota
				// Therefore, return the extra reserved quota back to the user account
				reservedRemained := reservedQuota - int64(serviceUsageRsp.ServiceRating.Price)
				ccr.RequestedAction = charging_datatype.REFUND_ACCOUNT
				ccr.MultipleServicesCreditControl = &charging_datatype.MultipleServicesCreditControl{
					RatingGroup: datatype.Unsigned32(rg),
					RequestedServiceUnit: &charging_datatype.RequestedServiceUnit{
						CCTotalOctets: datatype.Unsigned64(reservedRemained),
					},
				}
			} else {
				// The final consumed quota exceed the reserved quota
				// Deduct the extra consumed quota from the user account
				extraConsumed := int64(serviceUsageRsp.ServiceRating.Price) - reservedQuota
				ccr.RequestedAction = charging_datatype.DIRECT_DEBITING
				ccr.CcRequestType = charging_datatype.TERMINATION_REQUEST
				ccr.MultipleServicesCreditControl = &charging_datatype.MultipleServicesCreditControl{
					RatingGroup: datatype.Unsigned32(rg),
					UsedServiceUnit: &charging_datatype.UsedServiceUnit{
						CCTotalOctets: datatype.Unsigned64(extraConsumed),
					},
				}
			}

			accountRequest = ccr
			reservedQuota = 0

			unitInformation.Triggers = append(unitInformation.Triggers,
				models.Chf_ConvCharging_Trigger{
					TriggerType:     models.Chf_ConvCharging_TriggerType_QUOTA_EXHAUSTED,
					TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
				},
			)
			unitInformation.GrantedUnit = &models.Chf_ConvCharging_GrantedUnit{
				TotalVolume:    int32(0),
				DownlinkVolume: int32(0),
				UplinkVolume:   int32(0),
			}
		}
		step := &charging.SettlementStep{
			UsageIndex: unitUsageNum, QuotaKey: key, RatingGroup: rg, UPFID: unitUsage.UPFID,
			Before: before, Planned: charging.QuotaState{
				Known: true, Reserved: reservedQuota, RatingType: ratingType, UnitCost: unitCost,
				Exhausted: exhausted, Closed: closed,
			},
			Ratings: ratings, AccountRequest: accountRequest, AddGrantedUnits: addGrantedUnits,
			UnitInformation: unitInformation, PartialRecord: partialRecord,
			ThresholdRate: ue.VolumeThresholdRate, AdjustThreshold: thresholdIndex >= 0,
		}
		if addGrantedUnits {
			// Freeze both response branches before ABMF can return a final grant.
			finalInformation := unitInformation
			finalInformation.FinalUnitIndication = &models.Chf_ConvCharging_FinalUnitIndication{
				FinalUnitAction: models.Chf_ConvCharging_FinalUnitAction_TERMINATE,
			}
			finalInformation.Triggers = append([]models.Chf_ConvCharging_Trigger(nil), unitInformation.Triggers...)
			if thresholdIndex >= 0 {
				finalInformation.Triggers = append(finalInformation.Triggers[:thresholdIndex],
					finalInformation.Triggers[thresholdIndex+1:]...)
				finalInformation.VolumeQuotaThreshold = previousThreshold
			}
			step.FinalUnitInformation = &finalInformation
		}
		if err := p.saveSettlementStep(record, step); err != nil {
			return nil, false, err
		}
		outcome, executeErr := p.executeSettlementStep(ue, step)
		if executeErr != nil {
			return nil, false, executeErr
		}
		unitInformation = outcome.UnitInformation

		if err := p.saveSettlementOutcome(record, unitUsageNum, outcome); err != nil {
			return nil, false, err
		}
		if ue.Quotas == nil {
			ue.Quotas = make(map[charging.QuotaKey]charging.QuotaState)
		}
		after := outcome.After
		if record != nil {
			var applyErr error
			after, applyErr = p.applyQuota(record, unitUsageNum)
			if applyErr != nil {
				return nil, false, applyErr
			}
		}
		ue.Quotas[key] = after
		multipleUnitInformation = append(multipleUnitInformation, unitInformation)
	}

	return multipleUnitInformation, partialRecord, nil
}
