package context

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/fiorix/go-diameter/diam/sm"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/idgenerator"
)

var chfCtx *CHFContext

func init() {
	chfCtx = new(CHFContext)
	chfCtx.Name = "chf"
	chfCtx.UriScheme = models.UriScheme_HTTPS
	chfCtx.NfService = make(map[models.ServiceName]models.NfService)
	chfCtx.RatingSessionGenerator = idgenerator.NewGenerator(1, math.MaxUint32)
}

type CHFContext struct {
	NfId                      string
	Name                      string
	UriScheme                 models.UriScheme
	BindingIPv4               string
	RegisterIPv4              string
	SBIPort                   int
	NfService                 map[models.ServiceName]models.NfService
	RecordSequenceNumber      map[string]int64
	LocalRecordSequenceNumber uint64
	NrfUri                    string
	UePool                    sync.Map

	RatingAddr             string
	RatingCfg              *sm.Settings
	RatingSessionGenerator *idgenerator.IDGenerator
}

// Create new CHF context
func CHF_Self() *CHFContext {
	return chfCtx
}

func (c *CHFContext) GetIPv4Uri() string {
	return fmt.Sprintf("%s://%s:%d", c.UriScheme, c.RegisterIPv4, c.SBIPort)
}

// Init NfService with supported service list ,and version of services
func (c *CHFContext) InitNFService(serviceList []factory.Service, version string) {
	tmpVersion := strings.Split(version, ".")
	versionUri := "v" + tmpVersion[0]
	for index, service := range serviceList {
		name := models.ServiceName(service.ServiceName)
		c.NfService[name] = models.NfService{
			ServiceInstanceId: strconv.Itoa(index),
			ServiceName:       name,
			Versions: &[]models.NfServiceVersion{
				{
					ApiFullVersion:  version,
					ApiVersionInUri: versionUri,
				},
			},
			Scheme:          c.UriScheme,
			NfServiceStatus: models.NfServiceStatus_REGISTERED,
			ApiPrefix:       c.GetIPv4Uri(),
			IpEndPoints: &[]models.IpEndPoint{
				{
					Ipv4Address: c.RegisterIPv4,
					Transport:   models.TransportProtocol_TCP,
					Port:        int32(c.SBIPort),
				},
			},
			SupportedFeatures: service.SuppFeat,
		}
	}
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
