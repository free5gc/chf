package context

import (
	"sync"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/avp"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm"
	"github.com/free5gc/CDRUtil/cdrType"
	"github.com/free5gc/chf/pkg/factory"
	"github.com/free5gc/openapi/models"

	"github.com/juju/fslock"
)

type ChfUe struct {
	Supi string

	QuotaValidityTime   int32
	VolumeLimit         int32
	VolumeLimitPDU      int32
	VolumeThresholdRate float32

	NotifyUri string
	// Rating
	RatingGroups []int32

	// For debug
	AccumulateUsage models.UsedUnitContainer

	RecordSequenceNumber int64
	Cdr                  map[string]*cdrType.CHFRecord

	// ABMF
	ReservedQuota map[int32]int64
	UnitCost      map[int32]uint32

	// Rating
	RatingClient *sm.Client
	RatingMux    *sm.StateMachine
	RatingChan   chan *diam.Message

	// lock
	CdrFileLock fslock.Lock
	CULock      sync.Mutex
}

func (ue *ChfUe) FindRatingGroup(ratingGroup int32) bool {
	for _, rg := range ue.RatingGroups {
		if rg == ratingGroup {
			return true
		}
	}
	return false
}
func (ue *ChfUe) init() {
	config := factory.ChfConfig

	ue.Cdr = make(map[string]*cdrType.CHFRecord)
	ue.VolumeLimit = config.Configuration.VolumeLimit
	ue.VolumeLimitPDU = config.Configuration.VolumeLimitPDU
	ue.QuotaValidityTime = config.Configuration.QuotaValidityTime
	ue.VolumeThresholdRate = config.Configuration.VolumeThresholdRate

	// This needed to be added if rating server do not locat in the same machine
	// err := dict.Default.Load(bytes.NewReader([]byte(rate_dict.RateDictionary)))
	// if err != nil {
	// 	log.Fatal(err)
	// }
	ue.ReservedQuota = make(map[int32]int64)
	ue.UnitCost = make(map[int32]uint32)

	ue.RatingChan = make(chan *diam.Message)
	// Create the state machine (it's a diam.ServeMux) and client.
	ue.RatingMux = sm.New(chfCtx.RatingCfg)
	ue.RatingClient = &sm.Client{
		Dict:               dict.Default,
		Handler:            ue.RatingMux,
		MaxRetransmits:     3,
		RetransmitInterval: time.Second,
		EnableWatchdog:     true,
		WatchdogInterval:   5 * time.Second,
		AuthApplicationID: []*diam.AVP{
			// Advertise support for credit control application
			diam.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(4)), // RFC 4006
		},
	}

}
