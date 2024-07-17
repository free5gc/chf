package consumer

import (
	"context"
	"strings"
	"sync"
	"time"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"

	// "github.com/free5gc/openapi/Nnrf_NFDiscovery"
	Nnrf_NFDiscovery "github.com/free5gc/openapi-r17/nrf/NFDiscovery"
	"github.com/free5gc/openapi-r17/nrf/NFManagement"
	Nnrf_NFManagement "github.com/free5gc/openapi-r17/nrf/NFManagement"

	// "github.com/free5gc/openapi/Nnrf_NFManagement"
	// R17

	// "github.com/free5gc/openapi/models"
	"github.com/free5gc/openapi-r17/models"
	"github.com/pkg/errors"
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
		defer s.nfMngmntMu.RUnlock()
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
		defer s.nfDiscMu.RUnlock()
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
	nrfUri string, targetNfType, requestNfType models.NfType, param Nnrf_NFDiscovery.SearchNFInstancesParamOpts,
) (
	*models.SearchResult, error,
) {
	// Set client and set url
	chfContext := s.consumer.Context()

	client := s.getNFDiscClient(chfContext.NrfUri)

	ctx, _, err := chf_context.GetSelf().GetTokenCtx(models.ServiceName_NNRF_DISC, models.NrfNfManagementNfType_NRF)
	if err != nil {
		return nil, err
	}

	res, err := client.NFInstancesStoreApi.SearchNFInstances(ctx, &param)
	result := res.SearchResult
	if err != nil {
		logger.ConsumerLog.Errorf("SearchNFInstances failed: %+v", err)
	}

	return &result, nil
}

func (s *nnrfService) SendDeregisterNFInstance() (problemDetails *models.ProblemDetails, err error) {
	logger.ConsumerLog.Infof("Send Deregister NFInstance")

	ctx, pd, err := chf_context.GetSelf().GetTokenCtx(models.ServiceName_NNRF_NFM, models.NrfNfManagementNfType_NRF)
	if err != nil {
		return pd, err
	}

	chfContext := s.consumer.Context()
	client := s.getNFManagementClient(chfContext.NrfUri)

	var request *Nnrf_NFManagement.DeregisterNFInstanceRequest
	request.SetNfInstanceID(chfContext.NfId)

	_, err = client.NFInstanceIDDocumentApi.DeregisterNFInstance(ctx, request)

	return problemDetails, err
}

func (s *nnrfService) RegisterNFInstance(ctx context.Context) (
	resouceNrfUri string, retrieveNfInstanceID string, err error) {
	chfContext := s.consumer.Context()

	client := s.getNFManagementClient(chfContext.NrfUri)
	nfProfile, err := s.buildNfProfile(chfContext)
	if err != nil {
		return "", "", errors.Wrap(err, "RegisterNFInstance buildNfProfile()")
	}

	var nf models.NrfNfManagementNfProfile
	var res *NFManagement.RegisterNFInstanceResponse
	var registerNFInstanceRequest *NFManagement.RegisterNFInstanceRequest
	registerNFInstanceRequest.SetNfInstanceID(chfContext.NfId)
	registerNFInstanceRequest.SetNrfNfManagementNfProfile(nfProfile)
	for {
		res, err = client.NFInstanceIDDocumentApi.RegisterNFInstance(ctx, registerNFInstanceRequest)
		nf = res.NrfNfManagementNfProfile
		if err != nil || res == nil {
			logger.ConsumerLog.Errorf("CHF register to NRF Error[%v]", err)
			time.Sleep(2 * time.Second)
			continue
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

func (s *nnrfService) buildNfProfile(chfContext *chf_context.CHFContext) (profile models.NrfNfManagementNfProfile, err error) {
	profile.NfInstanceId = chfContext.NfId
	profile.NfType = models.NrfNfManagementNfType_CHF
	profile.NfStatus = models.NrfNfManagementNfStatus_REGISTERED
	profile.Ipv4Addresses = append(profile.Ipv4Addresses, chfContext.RegisterIPv4)
	services := []models.NrfNfManagementNfService{}
	for _, nfService := range chfContext.NfService {
		services = append(services, nfService)
	}
	if len(services) > 0 {
		profile.NfServices = services
	}
	profile.ChfInfo = &models.ChfInfo{
		// Todo
		// SupiRanges: &[]models.SupiRange{
		// 	{
		// 		//from TS 29.510 6.1.6.2.9 example2
		//		//no need to set supirange in this moment 2019/10/4
		// 		Start:   "123456789040000",
		// 		End:     "123456789059999",
		// 		Pattern: "^imsi-12345678904[0-9]{4}$",
		// 	},
		// },
	}
	return
}
