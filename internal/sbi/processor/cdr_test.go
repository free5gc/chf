package processor

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/free5gc/chf/cdr/asn"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

func TestCDRFileUsesBoundedNameForLongSubscriber(t *testing.T) {
	ueid := "imsi-" + strings.Repeat("b", 1024)
	fileName := buildCdrFileName(ueid)
	require.True(t, strings.HasPrefix(fileName, "/tmp/chf-"))
	require.True(t, strings.HasSuffix(fileName, ".cdr"))
	require.LessOrEqual(t, len(filepath.Base(fileName)), 255)
	t.Cleanup(func() {
		err := os.Remove(fileName)
		if err != nil && !os.IsNotExist(err) {
			t.Errorf("remove %s: %v", fileName, err)
		}
	})

	err := dumpCdrFile(ueid, nil)
	require.NoError(t, err)

	_, err = os.Stat(fileName)
	require.NoError(t, err)
}

func TestOpenCDRPartialRecordReturnsErrorWhenSessionMissing(t *testing.T) {
	p := &Processor{}

	ue := &chf_context.ChfUe{
		Supi: "imsi-208930000000951",
		Cdr:  map[string]*cdrType.CHFRecord{},
	}

	cdr, err := p.OpenCDR(models.Chf_ConvCharging_ChargingDataRequest{}, ue, "missing-session", true)
	require.Nil(t, cdr)
	require.Error(t, err)
}

func cdrUsageTotals(records []*cdrType.CHFRecord) (int64, int) {
	var volume int64
	var entries int
	for _, record := range records {
		for _, usage := range record.ChargingFunctionRecord.ListOfMultipleUnitUsage {
			for _, used := range usage.UsedUnitContainers {
				entries++
				if used.DataTotalVolume != nil {
					volume += used.DataTotalVolume.Value
				}
			}
		}
	}
	return volume, entries
}

func TestCDRRolloverPreservesUsageAndRejectsOversizedSingleEntry(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	state := &charging.CDRState{Subscriber: ue.Supi, Current: ue.Cdr[ref]}
	usage := onlineQuotaUsage(strings.Repeat("u", 12000), 1, false)
	request.MultipleUnitUsage = nil
	for range 7 {
		request.MultipleUnitUsage = append(request.MultipleUnitUsage, usage)
	}
	stamp := time.Now().UTC()
	change, err := p.prepareCDRChange(state, request, chargingUpdateOperation, false, stamp)
	require.NoError(t, err)
	require.NotEmpty(t, change.Closed)
	records := append(change.Closed, change.After.Current)
	volume, entries := cdrUsageTotals(records)
	require.Equal(t, int64(7), volume)
	require.Equal(t, 7, entries)
	for _, record := range records {
		encoded, encodeErr := asn.BerMarshalWithParams(record, "explicit,choice")
		require.NoError(t, encodeErr)
		require.LessOrEqual(t, len(encoded), math.MaxUint16)
	}
	require.NoError(t, dumpCdrFile(ue.Supi, records))
	before, err := os.ReadFile(buildCdrFileName(ue.Supi))
	require.NoError(t, err)
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
		onlineQuotaUsage(strings.Repeat("u", 70000), 1, false),
	}
	_, err = p.prepareCDRChange(
		state, request, chargingUpdateOperation, false, stamp)
	require.Error(t, err)
	require.Empty(t, state.Current.ChargingFunctionRecord.ListOfMultipleUnitUsage)
	err = dumpCdrFile(ue.Supi, []*cdrType.CHFRecord{nil})
	require.Error(t, err)
	after, err := os.ReadFile(buildCdrFileName(ue.Supi))
	require.NoError(t, err)
	require.Equal(t, before, after, "a failed replacement preserves the previous output")
}
