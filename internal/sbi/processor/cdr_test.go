package processor

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/free5gc/chf/cdr/asn"
	"github.com/free5gc/chf/cdr/cdrType"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/testutil"
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

func initializeTestCDR(t *testing.T, p *Processor, ue *chf_context.ChfUe, ref string) {
	t.Helper()
	require.NoError(t, p.journal.InitializeCDR(context.Background(), ref, &charging.CDRState{
		Subscriber: ue.Supi, NotifyURI: ue.NotifyUri, Current: ue.Cdr[ref],
	}))
}

type lostCompletionJournal struct{ charging.Journal }

func (j *lostCompletionJournal) Finish(
	ctx context.Context, record *charging.Record, result charging.Result, state string,
) error {
	if err := j.Journal.Finish(ctx, record, result, state); err != nil {
		return err
	}
	return errors.New("completion acknowledgement lost")
}

func TestMongoCDRReconstructionUsesCompletedRequestsInProcessingOrder(t *testing.T) {
	db := testutil.Mongo(t)
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
	ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	initializeTestCDR(t, p, ue, ref)
	var initial bson.M
	require.NoError(t, db.Collection("chf.chargingSessions").FindOne(
		context.Background(), bson.M{"_id": ref}).Decode(&initial))
	request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	stamp := time.Date(2026, time.October, 23, 14, 30, 0, 123456789, time.FixedZone("UTC+8", 8*60*60))
	request.InvocationTimeStamp = &stamp
	request.Triggers = []models.Chf_ConvCharging_Trigger{{
		TriggerType:     models.Chf_ConvCharging_TriggerType_VOLUME_LIMIT,
		TriggerCategory: models.Chf_ConvCharging_TriggerCategory_IMMEDIATE_REPORT,
	}}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	// Invocation numbers are identities; actual processing order is preserved by
	// predecessor links, without assuming monotonically increasing client numbers.
	request.InvocationSequenceNumber = 3
	request.Triggers = nil
	request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume = 30
	_, problem = p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	request.InvocationSequenceNumber = 99
	request.MultipleUnitUsage[0].RequestedUnit = nil
	request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume = 20
	p.journal = &lostCompletionJournal{Journal: p.journal}
	require.NotNil(t, p.ChargingDataRelease(request, ref))
	require.Nil(t, p.ChargingDataRelease(request, ref), "a committed completion replays despite its lost acknowledgement")
	before, err := os.ReadFile(buildCdrFileName(ue.Supi))
	require.NoError(t, err)
	counter := chf_context.GetSelf().LocalRecordSequenceNumber
	restarted := &Processor{journal: charging.NewJournal(db)}
	freshUE := &chf_context.ChfUe{Supi: ue.Supi}
	for range 2 {
		require.NoError(t, restarted.RestoreCDRs(freshUE))
	}
	volume, entries := cdrUsageTotals(freshUE.Records)
	require.Equal(t, int64(90), volume)
	require.Equal(t, 3, entries)
	require.Len(t, freshUE.Records, 2)
	require.True(t, freshUE.CDRProgress[ref].Closed)
	after, err := os.ReadFile(buildCdrFileName(ue.Supi))
	require.NoError(t, err)
	require.Equal(t, before, after, "replay preserves usage, partial boundaries, timestamps and local numbers")
	require.Equal(t, counter, chf_context.GetSelf().LocalRecordSequenceNumber, "replay does not allocate new numbers")
	requireQuotaBalance(t, db, ue.Supi, 910)
	var session bson.M
	require.NoError(t, db.Collection("chf.chargingSessions").FindOne(
		context.Background(), bson.M{"_id": ref}).Decode(&session))
	require.Equal(t, initial["cdrInitial"], session["cdrInitial"], "normal reports never rewrite the initial seed")
	require.NotContains(t, session, "cdrApplied")
	require.NotContains(t, session, "cdr")
	histories, err := charging.NewJournal(db).LoadCDRHistory(context.Background(), ue.Supi)
	require.NoError(t, err)
	require.Len(t, histories, 1)
	require.Len(t, histories[0].Events, 3)
	for _, record := range histories[0].Events {
		require.NotNil(t, record.CDR)
	}
	// A new UE still rejects further charging after reconstructing Release.
	chf_context.GetSelf().UePool.Store(ue.Supi, freshUE)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	_, problem = restarted.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusNotFound), problem.Status)
	requireQuotaBalance(t, db, ue.Supi, 910)
}

func TestMongoCDRPendingFileFailureRebuildsPrefixWithoutRepeatingAccounting(t *testing.T) {
	db := testutil.Mongo(t)
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	useMongoQuotaAccount(t, p, db, request.SubscriberIdentifier, "1000")
	ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	initializeTestCDR(t, p, ue, ref)
	calls := countAccountCalls(p)
	request.MultipleUnitUsage[0].RequestedUnit = &models.Chf_ConvCharging_RequestedUnit{TotalVolume: 100}
	_, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage[0].UsedUnitContainer[0].TotalVolume = 30
	p.cdrWriter = func(string, []*cdrType.CHFRecord) error { return errors.New("CDR disk unavailable") }
	_, problem = p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	var pending charging.Record
	require.NoError(t, db.Collection("chf.chargingRequests").FindOne(context.Background(), bson.M{
		"_id": (charging.Identity{Session: ref, Sequence: request.InvocationSequenceNumber}).ID(),
	}).Decode(&pending))
	require.Equal(t, charging.Uncertain, pending.State)
	require.Nil(t, pending.CDR, "CDR metadata is committed only with the completed response")
	restarted := &Processor{journal: charging.NewJournal(db)}
	freshUE := &chf_context.ChfUe{Supi: ue.Supi}
	require.NoError(t, restarted.RestoreCDRs(freshUE))
	volume, entries := cdrUsageTotals(freshUE.Records)
	require.Equal(t, int64(40), volume, "uncertain invocations are not silently treated as completed CDR events")
	require.Equal(t, 1, entries)
	// A future recovery worker first reconciles accounting. At the CDR stage it
	// can replay this pending original request once, with no RF/ABMF clients.
	for range 2 {
		require.NoError(t, restarted.applyChargingCDR(freshUE, &pending, false))
	}
	volume, entries = cdrUsageTotals(freshUE.Records)
	require.Equal(t, int64(70), volume)
	require.Equal(t, 2, entries)
	require.Equal(t, int32(1), calls.Load())
	requireQuotaBalance(t, db, ue.Supi, 860)
	_, _, err := charging.NewJournal(db).Begin(context.Background(), charging.Identity{
		Session: ref, Sequence: request.InvocationSequenceNumber + 1, Operation: chargingReleaseOperation,
	})
	require.ErrorIs(t, err, charging.ErrPending)
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
	metadata := &charging.CDRMetadata{Stamp: time.Now().UTC().Format(time.RFC3339Nano)}
	change, err := p.prepareCDRChange(state, request, chargingUpdateOperation, metadata, false)
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
	replayed, err := p.prepareCDRChange(state, request, chargingUpdateOperation, metadata, true)
	require.NoError(t, err)
	require.Equal(t, change.After, replayed.After)
	require.Equal(t, change.Closed, replayed.Closed)
	require.NoError(t, dumpCdrFile(ue.Supi, records))
	before, err := os.ReadFile(buildCdrFileName(ue.Supi))
	require.NoError(t, err)
	request.MultipleUnitUsage = []models.Chf_ConvCharging_MultipleUnitUsage{
		onlineQuotaUsage(strings.Repeat("u", 70000), 1, false),
	}
	_, err = p.prepareCDRChange(
		state, request, chargingUpdateOperation, &charging.CDRMetadata{Stamp: metadata.Stamp}, false)
	require.Error(t, err)
	require.Empty(t, state.Current.ChargingFunctionRecord.ListOfMultipleUnitUsage)
	err = dumpCdrFile(ue.Supi, []*cdrType.CHFRecord{nil})
	require.Error(t, err)
	after, err := os.ReadFile(buildCdrFileName(ue.Supi))
	require.NoError(t, err)
	require.Equal(t, before, after, "a failed replacement preserves the previous output")
}

func TestMongoCDRCreatePersistsInitialUsageForRecovery(t *testing.T) {
	db := testutil.Mongo(t)
	p, request, _ := chargingRequestSession(t)
	ue, _ := chf_context.GetSelf().ChfUeFindBySupi(request.SubscriberIdentifier)
	ue.Cdr, ue.Records = make(map[string]*cdrType.CHFRecord), nil
	p.journal = charging.NewJournal(db)
	_, location, problem := p.ChargingDataCreate(request)
	require.Nil(t, problem)
	ref := location[strings.LastIndex(location, "/")+1:]
	restarted := &Processor{journal: charging.NewJournal(db)}
	freshUE := &chf_context.ChfUe{Supi: request.SubscriberIdentifier}
	require.NoError(t, restarted.RestoreCDRs(freshUE))
	volume, entries := cdrUsageTotals(freshUE.Records)
	require.Equal(t, int64(40), volume)
	require.Equal(t, 1, entries)
	require.NotNil(t, freshUE.Cdr[ref])
}
