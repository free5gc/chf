package context

import (
	"sync"

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
}
