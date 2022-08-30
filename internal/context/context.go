package context

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/free5gc/CDRUtil/cdrType"
	"github.com/free5gc/TarrifUtil/tarrifType"
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
	chfCtx.ChargingSession = make(map[string]*cdrType.CHFRecord)
	chfCtx.RatingGroupMonetaryQuotaMap = make(map[int32]int32)
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
	LocalRecordSequenceNumber uint64
	NrfUri                    string
	UePool                    sync.Map
	ChargingSession           map[string]*cdrType.CHFRecord

	OnlineCharging bool
	// Rating
	Tarrif tarrifType.CurrentTariff
	// AMBF
	RatingSessionGenerator      *idgenerator.IDGenerator
	RatingGroupMonetaryQuotaMap map[int32]int32
	InitMonetaryQuota           int32

	RatingGroupMonetaryQuotaMapMutex sync.RWMutex
}

// Create new CHF context
func CHF_Self() *CHFContext {
	return chfCtx
}

func (c *CHFContext) GetIPv4Uri() string {
	return fmt.Sprintf("%s://%s:%d", c.UriScheme, c.RegisterIPv4, c.SBIPort)
}

func (c *CHFContext) InitTarrif(tarrif *factory.Tarrif) {
	c.Tarrif = tarrifType.CurrentTariff{
		RateElement: &tarrifType.RateElement{
			ChargeReasonCode: &tarrifType.ChargeReasonCode{
				Value: tarrif.ChargeReasonCode.Value,
			},
			UnitCost: &tarrifType.UnitCost{
				ValueDigits: tarrif.UnitCost.ValueDigits,
				Exponent:    tarrif.UnitCost.Exponent,
			},
			CCUnitType: &tarrifType.CCUnitType{
				Value: tarrif.CCUnitType.Value,
			},
			UnitQuotaThreshold: tarrif.UnitQuotaThreshold,
		},
	}
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

// Allocate CHF Ue with supi and add to chf Context and returns allocated ue
func (c *CHFContext) NewCHFUe(Supi string) (*ChfUe, error) {
	if _, ok := c.ChfUeFindBySupi(Supi); ok {
		return nil, fmt.Errorf("Ue exist")
	}
	if strings.HasPrefix(Supi, "imsi-") {
		newUeContext := &ChfUe{}
		newUeContext.Supi = Supi
		c.UePool.Store(Supi, newUeContext)
		return newUeContext, nil
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
