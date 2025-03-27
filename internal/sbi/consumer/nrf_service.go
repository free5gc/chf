package consumer

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
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

func (s *nnrfService) buildNfProfile(
	chfContext *chf_context.CHFContext,
) (profile models.NrfNfManagementNfProfile, err error) {

	var nfList []models.NrfNfManagementNfType
	nfList = append(nfList, models.NrfNfManagementNfType_AMF)
	nfList = append(nfList, models.NrfNfManagementNfType_SMF)

	var allowedplmindlist []models.PlmnId
	var allowedplmnid models.PlmnId
	allowedplmnid.Mcc = "001"
	allowedplmnid.Mnc = "01"
	allowedplmindlist = append(allowedplmindlist, allowedplmnid)

	var perplmnsnssailist []models.PlmnSnssai
	var plmnsnssai models.PlmnSnssai
	var snssailist []models.ExtSnssai
	var snssai models.ExtSnssai
	var snssai2 models.ExtSnssai

	snssai.Sd = "19CDE1"
	snssai.Sst = 1
	snssailist = append(snssailist, snssai)

	snssai2.Sst = 1
	snssai2.WildcardSd = true
	snssailist = append(snssailist, snssai2)

	plmnsnssai.PlmnId = &allowedplmnid
	perplmnsnssailist = append(perplmnsnssailist, plmnsnssai)

	// var nfservicelist map[string]models.NrfNfManagementNfService
	// map [2b31b20b-ba79-4010-915b-4fd3a4d69842 : ]

	// var nfservice models.NrfNfManagementNfService

	// nfservice.AllowedNfTypes=nfList
	// nfservice.AllowedOperationsPerNfType= map[string][]string{
	// 	SMF: {nsmf-comm},
	// 	AMF: {namf-comm},
	// }
	// nfservice.AllowedPlmns = allowedplmindlist
	// nfservice.ApiPrefix = "/nchf-comm/v1"
	// nfservice.Fqdn = "service-enterprise1-slice1-chf.ns-enterprise1.svc.cluster.local:8080"
	// nfservice.InterPlmnFqdn = "0200c1.chf.5gc.mnc01.mcc001.3gppnetwork.org"
	// nfservice.NfServiceStatus = models.NrfNfManagementNfStatus_REGISTERED
	// nfservice.Scheme = "http"
	// nfservice.ServiceInstanceId = "2b31b20b-ba79-4010-915b-4fd3a4d69842"
	// nfservice.ServiceName = "nchf-comm"
	// nfservice.SupportedFeatures = "1"

	profile.NfInstanceId = chfContext.NfId
	profile.NfType = models.NrfNfManagementNfType_CHF
	profile.NfStatus = models.NrfNfManagementNfStatus_REGISTERED
	profile.Ipv4Addresses = append(profile.Ipv4Addresses, chfContext.RegisterIPv4)
	profile.AllowedNfTypes = nfList
	profile.AllowedPlmns = allowedplmindlist
	// profile.ChfInfo =
	profile.Fqdn = "0200c1.chf.5gc.mnc01.mcc001.3gppnetwork.org"
	profile.InterPlmnFqdn = "0200c1.chf.5gc.mnc01.mcc001.3gppnetwork.org"
	profile.PerPlmnSnssaiList = perplmnsnssailist
	profile.PlmnList = allowedplmindlist
	profile.SNssais = snssailist

	services := []models.NrfNfManagementNfService{}

	for _, nfService := range chfContext.NfService {
		services = append(services, nfService)
	}
	if len(services) > 0 {
		profile.NfServices = services
		profile.NfServiceList = map[string]models.NrfNfManagementNfService{
			chfContext.NfId: services[0],
		}
	}
	profile.ChfInfo = &models.ChfInfo{
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
	return
}
