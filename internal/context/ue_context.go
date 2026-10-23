package context

import (
	"sync"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/avp"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	"github.com/free5gc/chf/pkg/factory"
)

type ChfUe struct {
	Supi        string
	Quotas      map[charging.QuotaKey]charging.QuotaState
	CDRProgress map[string]charging.CDRProgress

	QuotaValidityTime    int32
	VolumeLimit          int32
	VolumeLimitPDU       int32
	VolumeThresholdRate  float32
	NotifyUri            string
	RecordSequenceNumber int64

	// ABMF
	AbmfClient    *sm.Client
	AbmfMux       *sm.StateMachine
	AcctChan      chan *diam.Message
	AcctSessionId uint32

	// Rating
	RatingClient  *sm.Client
	RatingMux     *sm.StateMachine
	RatingChan    chan *diam.Message
	RateSessionId uint32
	Records       []*cdrType.CHFRecord

	// lock
	Cdr    map[string]*cdrType.CHFRecord
	CULock sync.Mutex
}

// RechargeRatingGroup reactivates live quotas without reopening final reports.
// Callers serialize changes with CULock.
func (ue *ChfUe) RechargeRatingGroup(ratingGroup int32) bool {
	active := false
	for key, state := range ue.Quotas {
		if key.RatingGroup != ratingGroup || state.Closed {
			continue
		}
		state.Exhausted = false
		state.RatingType = charging_datatype.REQ_SUBTYPE_RESERVE
		ue.Quotas[key] = state
		active = true
	}
	return active
}

func (ue *ChfUe) init() {
	config := factory.ChfConfig
	ue.Records = []*cdrType.CHFRecord{}
	ue.Cdr = make(map[string]*cdrType.CHFRecord)
	ue.Records = []*cdrType.CHFRecord{}
	ue.VolumeLimit = config.Configuration.VolumeLimit
	ue.VolumeLimitPDU = config.Configuration.VolumeLimitPDU
	ue.QuotaValidityTime = config.Configuration.QuotaValidityTime
	ue.VolumeThresholdRate = config.Configuration.VolumeThresholdRate
	// This needed to be added if rating server do not locate in the same machine
	// err := dict.Default.Load(bytes.NewReader([]byte(charging_dict.RateDictionary)))
	// if err != nil {
	// 	log.Fatal(err)
	// }
	ue.Quotas = make(map[charging.QuotaKey]charging.QuotaState)

	ue.RatingChan = make(chan *diam.Message)
	ue.AcctChan = make(chan *diam.Message)
	// Create the state machine (it's a diam.ServeMux) and client.
	ue.RatingMux = sm.New(chfContext.RatingCfg)
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

	ue.AbmfMux = sm.New(chfContext.AbmfCfg)
	ue.AbmfClient = &sm.Client{
		Dict:               dict.Default,
		Handler:            ue.AbmfMux,
		MaxRetransmits:     3,
		RetransmitInterval: time.Second,
		EnableWatchdog:     true,
		WatchdogInterval:   5 * time.Second,
		AuthApplicationID: []*diam.AVP{
			// Advertise support for credit control application
			diam.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(4)), // RFC 4006
		},
	}

	ue.RateSessionId = GenerateRatingSessionId()
	ue.AcctSessionId = GenerateAccountSessionId()
}
