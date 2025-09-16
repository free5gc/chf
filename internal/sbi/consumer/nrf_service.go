package consumer

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/openapi"
	"github.com/free5gc/openapi/models"
	Nnrf_NFDiscovery "github.com/free5gc/openapi/nrf/NFDiscovery"
	Nnrf_NFManagement "github.com/free5gc/openapi/nrf/NFManagement"
)

type nnrfService struct {
	consumer *Consumer

	nfMngmntMu sync.RWMutex
	nfDiscMu   sync.RWMutex

	nfMngmntClients map[string]*Nnrf_NFManagement.APIClient
	nfDiscClients   map[string]*Nnrf_NFDiscovery.APIClient
}

func (s *nnrfService) getNFManagementClient(uri string) *Nnrf_NFManagement.APIClient {
	if uri == "" {
		return nil
	}
	s.nfMngmntMu.RLock()
	client, ok := s.nfMngmntClients[uri]
	if ok {
		s.nfMngmntMu.RUnlock()
		return client
	}

	configuration := Nnrf_NFManagement.NewConfiguration()
	configuration.SetBasePath(uri)
	client = Nnrf_NFManagement.NewAPIClient(configuration)

	s.nfMngmntMu.RUnlock()
	s.nfMngmntMu.Lock()
	defer s.nfMngmntMu.Unlock()
	s.nfMngmntClients[uri] = client
	return client
}

func (s *nnrfService) getNFDiscClient(uri string) *Nnrf_NFDiscovery.APIClient {
	if uri == "" {
		return nil
	}
	s.nfDiscMu.RLock()
	client, ok := s.nfDiscClients[uri]
	if ok {
		s.nfDiscMu.RUnlock()
		return client
	}

	configuration := Nnrf_NFDiscovery.NewConfiguration()
	configuration.SetBasePath(uri)
	client = Nnrf_NFDiscovery.NewAPIClient(configuration)

	s.nfDiscMu.RUnlock()
	s.nfDiscMu.Lock()
	defer s.nfDiscMu.Unlock()

	s.nfDiscClients[uri] = client
	return client
}

func (s *nnrfService) SendSearchNFInstances(
	nrfUri string, targetNfType,
	requestNfType models.NrfNfManagementNfType,
	param Nnrf_NFDiscovery.SearchNFInstancesRequest,
) (
	*models.SearchResult, error,
) {
	chfContext := s.consumer.Context()

	client := s.getNFDiscClient(chfContext.NrfUri)

	ctx, _, err := s.consumer.Context().GetTokenCtx(models.ServiceName_NNRF_DISC, models.NrfNfManagementNfType_NRF)
	if err != nil {
		return nil, err
	}

	res, err := client.NFInstancesStoreApi.SearchNFInstances(ctx, &param)
	if err != nil || res == nil {
		logger.ConsumerLog.Errorf("SearchNFInstances failed: %+v", err)
		return nil, err
	}
	result := res.SearchResult
	return &result, nil
}

func (s *nnrfService) SendDeregisterNFInstance() (*models.ProblemDetails, error) {
	logger.ConsumerLog.Infof("Send Deregister NFInstance")

	ctx, pd, err := chf_context.GetSelf().GetTokenCtx(models.ServiceName_NNRF_NFM, models.NrfNfManagementNfType_NRF)
	if err != nil {
		return pd, err
	}

	chfContext := s.consumer.Context()
	client := s.getNFManagementClient(chfContext.NrfUri)
	request := &Nnrf_NFManagement.DeregisterNFInstanceRequest{
		NfInstanceID: &chfContext.NfId,
	}

	_, err = client.NFInstanceIDDocumentApi.DeregisterNFInstance(ctx, request)
	if apiErr, ok := err.(openapi.GenericOpenAPIError); ok {
		// API error
		if deregNfError, okDeg := apiErr.Model().(Nnrf_NFManagement.DeregisterNFInstanceError); okDeg {
			return &deregNfError.ProblemDetails, err
		}
		return nil, err
	}
	return nil, err
}

func (s *nnrfService) RegisterNFInstance(ctx context.Context) (
	resouceNrfUri string, retrieveNfInstanceID string, err error,
) {
	chfContext := s.consumer.Context()
	client := s.getNFManagementClient(chfContext.NrfUri)
	nfProfile, err := s.buildNfProfile(chfContext)
	if err != nil {
		return "", "", errors.Wrap(err, "RegisterNFInstance buildNfProfile()")
	}

	var nf models.NrfNfManagementNfProfile
	var res *Nnrf_NFManagement.RegisterNFInstanceResponse
	registerNFInstanceRequest := &Nnrf_NFManagement.RegisterNFInstanceRequest{
		NfInstanceID:             &chfContext.NfId,
		NrfNfManagementNfProfile: &nfProfile,
	}
	for {
		select {
		case <-ctx.Done():
			return "", "", errors.Errorf("Context Cancel before RegisterNFInstance")
		default:
		}
		res, err = client.NFInstanceIDDocumentApi.RegisterNFInstance(ctx, registerNFInstanceRequest)
		if err != nil || res == nil {
			logger.ConsumerLog.Errorf("CHF register to NRF Error[%v]", err)
			time.Sleep(2 * time.Second)
			continue
		}
		nf = res.NrfNfManagementNfProfile
		if nf.NfInstanceId == "" {
			chfContext.HeartBeatTimer = 60
		} else {
			chfContext.HeartBeatTimer = nf.HeartBeatTimer
		}

		// http.StatusOK
		if res.Location == "" {
			// NFUpdate
			break
		} else { // http.StatusCreated
			// NFRegister
			resourceUri := res.Location
			resouceNrfUri = resourceUri[:strings.Index(resourceUri, "/nnrf-nfm/")]
			retrieveNfInstanceID = resourceUri[strings.LastIndex(resourceUri, "/")+1:]

			oauth2 := false
			if nf.CustomInfo != nil {
				v, ok := nf.CustomInfo["oauth2"].(bool)
				if ok {
					oauth2 = v
					logger.MainLog.Infoln("OAuth2 setting receive from NRF:", oauth2)
				}
			}
			chf_context.GetSelf().OAuth2Required = oauth2
			if oauth2 && chf_context.GetSelf().NrfCertPem == "" {
				logger.CfgLog.Error("OAuth2 enable but no nrfCertPem provided in config.")
			}

			break
		}
	}
	return resouceNrfUri, retrieveNfInstanceID, err
}

func (s *nnrfService) PatchNFupdate(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	chfContext := s.consumer.Context()

	heartbeat := chfContext.HeartBeatTimer
	if heartbeat <= 0 {
		heartbeat = 60
	}
	periodSec := heartbeat - 5
	if periodSec < 5 {
		periodSec = 5
	}
	period := time.Duration(periodSec) * time.Second

	timeoutSec := heartbeat / 2
	if timeoutSec < 3 {
		timeoutSec = 3
	}
	perCallTimeout := time.Duration(timeoutSec) * time.Second
	lastSuccess := time.Time{}
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			cctx, cancel := context.WithTimeout(ctx, perCallTimeout)
			err := s.PatchNFInstance(cctx)
			cancel()

			if err == nil {
				lastSuccess = time.Now()
				continue
			}

			// Try to read HTTP status code
			status := 0
			var apiErr openapi.GenericOpenAPIError
			if errors.As(err, &apiErr) {
				status = apiErr.ErrorStatus
			}
			switch status {
			case 404, 410:
				logger.ConsumerLog.Warn("NF Instance Missing at NRF Re-registering")
				if base, nfID, rerr := s.RegisterNFInstance(ctx); rerr != nil {
					logger.ConsumerLog.Errorf("Re-registration failed: %v", rerr)
				} else if nfID != "" {
					if base != "" {
						chfContext.NrfUri = base
					}
					chfContext.NfId = nfID
					lastSuccess = time.Now()
				}

			default:
				logger.ConsumerLog.Errorf("PATCH failed %v", err)

				// If likely missed the window, re-register
				if !lastSuccess.IsZero() && time.Since(lastSuccess) > time.Duration(heartbeat)*time.Second {
					logger.ConsumerLog.Warn("Missed HeartBeat window Re-registering")
					if base, nfID, rerr := s.RegisterNFInstance(ctx); rerr != nil {
						logger.ConsumerLog.Errorf("Re-registration failed: %v", rerr)
					} else if nfID != "" {
						if base != "" {
							chfContext.NrfUri = base
						}
						chfContext.NfId = nfID
						lastSuccess = time.Now()
					}
				}
			}
		}
	}
}

func (s *nnrfService) PatchNFInstance(ctx context.Context) error {
	chf := s.consumer.Context()
	client := s.getNFManagementClient(chf.NrfUri)

	items := []models.PatchItem{
		{
			Op:    models.PatchOperation_REPLACE,
			Path:  "/nfStatus",
			Value: models.NrfNfManagementNfStatus_REGISTERED,
		},
	}

	req := &Nnrf_NFManagement.UpdateNFInstanceRequest{
		NfInstanceID: &chf.NfId,
		PatchItem:    items,
	}

	res, err := client.NFInstanceIDDocumentApi.UpdateNFInstance(ctx, req)
	if err == nil {
		if res != nil && res.NrfNfManagementNfProfile.HeartBeatTimer > 0 {
			chf.HeartBeatTimer = res.NrfNfManagementNfProfile.HeartBeatTimer
		}
		return nil
	}

	// 1) If the generated client already returned GenericOpenAPIError, pass it through.

	if val, ok := any(err).(openapi.GenericOpenAPIError); ok {
		return val
	}

	// 2) If it's the "undefined response type" case, build and return a GenericOpenAPIError (apiErr)
	//    by probing with a safe, idempotent GET to capture the HTTP status.
	//  to be removed when the generator is fixed to always return GenericOpenAPIError.
	if err.Error() == "undefined response type" {
		getURL := strings.TrimRight(chf.NrfUri, "/") + "/nnrf-nfm/v1/nf-instances/" + chf.NfId

		httpReq, mkErr := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
		if mkErr == nil {
			httpReq.Header.Set("Accept", "application/json, application/problem+json, text/plain")

			resp, doErr := http.DefaultClient.Do(httpReq)
			if doErr == nil {
				raw, err_read := io.ReadAll(resp.Body)
				if err_read != nil {
					logger.ConsumerLog.Errorf("HTTP response body cannot be read: %v", err_read)
				}
				err_close := resp.Body.Close()
				if err_close != nil {
					logger.ConsumerLog.Errorf("HTTP response body cannot be closed: %v", err_close)
				}
				apiErr := openapi.GenericOpenAPIError{
					RawBody:     raw,
					ErrorStatus: resp.StatusCode,
					ErrorModel:  string(raw),
				}
				return apiErr
			}
		}
	}
	return err
}

func (s *nnrfService) buildNfProfile(
	chfContext *chf_context.CHFContext,
) (profile models.NrfNfManagementNfProfile, err error) {
	var nfList []models.NrfNfManagementNfType
	nfList = append(nfList, models.NrfNfManagementNfType_AMF)
	nfList = append(nfList, models.NrfNfManagementNfType_SMF)

	var allowedplmndlist []models.PlmnId
	var snssailist []models.ExtSnssai
	var perPlmnSnssaiList []models.PlmnSnssai

	for _, allowedPlmn := range chfContext.PlmnSupportList {
		var perPlmnSnssai models.PlmnSnssai
		allowedplmndlist = append(allowedplmndlist, *allowedPlmn.PlmnId)
		perPlmnSnssai.PlmnId = allowedPlmn.PlmnId
		perPlmnSnssai.SNssaiList = allowedPlmn.SNssaiList
		perPlmnSnssaiList = append(perPlmnSnssaiList, perPlmnSnssai)
		for _, snssaiItem := range allowedPlmn.SNssaiList {
			if snssaiItem.Sst != 0 {
				switch snssaiItem.Sd {
				case "":
					snssaiItem.WildcardSd = true
					snssailist = append(snssailist, snssaiItem)
				default:
					snssailist = append(snssailist, snssaiItem)
				}
			}
		}
	}

	profile.AllowedNfTypes = nfList

	if len(allowedplmndlist) > 0 {
		profile.AllowedPlmns = allowedplmndlist
	}

	profile.Fqdn = "chf.5gc.mnc01.mcc001.3gppnetwork.org"
	profile.InterPlmnFqdn = "chf.5gc.mnc01.mcc001.3gppnetwork.org"
	profile.NfInstanceId = chfContext.NfId
	profile.NfStatus = models.NrfNfManagementNfStatus_REGISTERED
	profile.NfType = models.NrfNfManagementNfType_CHF
	profile.PerPlmnSnssaiList = perPlmnSnssaiList
	if len(allowedplmndlist) > 0 {
		profile.PlmnList = allowedplmndlist
	}
	profile.SNssais = snssailist
	// profile.Ipv4Addresses = append(profile.Ipv4Addresses, chfContext.RegisterIPv4)
	services := []models.NrfNfManagementNfService{}
	serviceId := uuid.New().String()

	for serviceName, nfService := range chfContext.NfService {
		nfService.AllowedNfTypes = nfList
		if len(allowedplmndlist) > 0 {
			nfService.AllowedPlmns = allowedplmndlist
		}
		nfService.ApiPrefix = factory.ConvergedChargingResUriPrefix
		nfService.Fqdn = chfContext.Fqdn + ":" + strconv.Itoa(chfContext.SBIPort)
		// nfService.Fqdn = "service-enterprise1-slice1-convergedcharging.ns-enterprise1.svc.cluster.local:8080"
		nfService.InterPlmnFqdn = "chf.convergedcharging.5gc.mnc01.mcc001.3gppnetwork.org"
		nfService.ServiceInstanceId = serviceId
		nfService.ServiceName = serviceName
		nfService.SupportedFeatures = "1"
		services = append(services, nfService)
	}
	if len(services) > 0 {
		profile.NfServices = services
		profile.NfServiceList = map[string]models.NrfNfManagementNfService{
			serviceId: services[0],
		}
	}
	profile.ChfInfo = &models.ChfInfo{
		GroupId:            chfContext.NfId,
		PrimaryChfInstance: chfContext.NfId,
		// Todo
		// SupiRanges: &[]models.SupiRange{
		// 	{
		// 		// from TS 29.510 6.1.6.2.9 example2
		//		// no need to set supirange in this moment 2019/10/4
		// 		Start:   "123456789040000",
		// 		End:     "123456789059999",
		// 		Pattern: "^imsi-12345678904[0-9]{4}$",
		// 	},
		// },
	}
	return profile, err
}
