package context

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fiorix/go-diameter/diam/sm"
	"github.com/google/uuid"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/openapi/oauth"
	"github.com/free5gc/util/idgenerator"
)

var chfContext CHFContext

func Init() {
	InitChfContext(&chfContext)
}

type NFContext interface {
	AuthorizationCheck(token string, serviceName models.Nrf_NFMgmt_ServiceName) error
}

var _ NFContext = &CHFContext{}

type CHFContext struct {
	NfId                      string
	Name                      string
	Url                       string
	UriScheme                 models.UriScheme
	BindingIPv4               string
	RegisterIPv4              string
	SBIPort                   int
	NfService                 map[models.Nrf_NFMgmt_ServiceName]models.Nrf_NFMgmt_NFService
	RecordSequenceNumber      map[string]int64
	LocalRecordSequenceNumber uint64
	NrfUri                    string
	NrfCertPem                string
	NrfNfInstanceID           string
	UePool                    sync.Map
	OAuth2Required            bool

	RatingCfg *sm.Settings
	AbmfCfg   *sm.Settings

	RatingSessionIdGenerator  *idgenerator.IDGenerator
	AccountSessionIdGenerator *idgenerator.IDGenerator
	sync.Mutex
}

func (c *CHFContext) AuthorizationCheck(token string, serviceName models.Nrf_NFMgmt_ServiceName) error {
	if !c.OAuth2Required {
		logger.UtilLog.Debugf("CHFContext::AuthorizationCheck: OAuth2 not required\n")
		return nil
	}

	logger.UtilLog.Debugf("CHFContext::AuthorizationCheck: token[%s] serviceName[%s]\n", token, serviceName)
	return oauth.VerifyOAuth(token, string(serviceName), oauth.AudiencePolicy{
		NFInstanceID: c.NfId, NFType: models.Nrf_NFMgmt_NFType_CHF,
	}, c.NrfNfInstanceID, c.NrfCertPem)
}

func (context *CHFContext) AddChfUeToUePool(ue *ChfUe, supi string) {
	if len(supi) == 0 {
		logger.CtxLog.Errorf("Supi is nil")
	}
	ue.Supi = supi
	context.UePool.Store(ue.Supi, ue)
}

// Allocate CHF Ue with supi and add to chf Context and returns allocated ue
func (context *CHFContext) NewCHFUe(supi string) (*ChfUe, error) {
	if ue, ok := context.ChfUeFindBySupi(supi); ok {
		return ue, nil
	}
	if strings.HasPrefix(supi, "imsi-") {
		ue := ChfUe{}
		ue.init()

		if supi != "" {
			context.AddChfUeToUePool(&ue, supi)
		}

		return &ue, nil
	} else {
		return nil, fmt.Errorf(" add Ue context fail ")
	}
}

func (context *CHFContext) ChfUeFindBySupi(supi string) (*ChfUe, bool) {
	if value, ok := context.UePool.Load(supi); ok {
		return value.(*ChfUe), ok
	}
	return nil, false
}

func GenerateRatingSessionId() uint32 {
	if id, err := chfContext.RatingSessionIdGenerator.Allocate(); err == nil {
		return uint32(id)
	}
	return 0
}

func GenerateAccountSessionId() uint32 {
	if id, err := chfContext.AccountSessionIdGenerator.Allocate(); err == nil {
		return uint32(id)
	}
	return 0
}

func GetSelf() *CHFContext {
	return &chfContext
}

func (c *CHFContext) GetSelfID() string {
	return c.NfId
}

func (c *CHFContext) GetTokenCtx(serviceName models.Nrf_NFMgmt_ServiceName, targetNF models.Nrf_NFMgmt_NFType) (
	context.Context, *models.ProblemDetails, error,
) {
	if !c.OAuth2Required {
		return context.TODO(), nil, nil
	}
	return oauth.GetTokenCtx(c.tokenRequest(serviceName, targetNF))
}

func (c *CHFContext) GetTokenCtxForNFInstance(serviceName models.Nrf_NFMgmt_ServiceName,
	targetNF models.Nrf_NFMgmt_NFType, targetNFInstanceID string,
) (context.Context, *models.ProblemDetails, error) {
	if !c.OAuth2Required {
		return context.TODO(), nil, nil
	}
	targetID, err := uuid.Parse(strings.TrimSpace(targetNFInstanceID))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid target NF instance ID: %w", err)
	}
	if targetID.Version() != 4 {
		return nil, nil, fmt.Errorf("invalid target NF instance ID: UUID must be version 4")
	}
	return oauth.GetTokenCtx(c.tokenRequestForNFInstance(serviceName, targetNF, targetNFInstanceID))
}

func (c *CHFContext) GetTokenCtxForNRF(serviceName models.Nrf_NFMgmt_ServiceName) (
	context.Context, *models.ProblemDetails, error,
) {
	return c.GetTokenCtxForNFInstance(serviceName, models.Nrf_NFMgmt_NFType_NRF, c.NrfNfInstanceID)
}

func (c *CHFContext) tokenRequest(serviceName models.Nrf_NFMgmt_ServiceName,
	targetNF models.Nrf_NFMgmt_NFType,
) oauth.TokenRequest {
	return oauth.TokenRequest{
		ConsumerNFType: models.Nrf_NFMgmt_NFType_CHF, ConsumerNFInstanceID: c.NfId,
		TargetNFType: targetNF, NRFURI: c.NrfUri, Scope: string(serviceName),
	}
}

func (c *CHFContext) tokenRequestForNFInstance(serviceName models.Nrf_NFMgmt_ServiceName,
	targetNF models.Nrf_NFMgmt_NFType, targetNFInstanceID string,
) oauth.TokenRequest {
	request := c.tokenRequest(serviceName, targetNF)
	request.TargetNFInstanceID = targetNFInstanceID
	return request
}

func (c *CHFContext) SetOAuth2Required(required bool) error {
	if !required {
		c.OAuth2Required = false
		c.NrfNfInstanceID = ""
		return nil
	}
	if strings.TrimSpace(c.NrfCertPem) == "" {
		return fmt.Errorf("OAuth2 enabled but NRF certificate path is empty")
	}
	if strings.TrimSpace(c.NrfUri) == "" {
		return fmt.Errorf("OAuth2 enabled but NRF URI is empty")
	}
	nrfNfInstanceID, err := oauth.NFInstanceIDFromCertificate(c.NrfCertPem)
	if err != nil {
		return fmt.Errorf("derive trusted NRF instance ID from certificate: %w", err)
	}
	c.NrfNfInstanceID = nrfNfInstanceID
	c.OAuth2Required = true
	return nil
}
