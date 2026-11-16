package processor

import (
	"errors"
	"math"
	"testing"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/sm"
	"github.com/stretchr/testify/require"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/cdr/cdrType"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

const (
	volumeTestSupi = "imsi-208930000000960"
	volumeTestRG   = int32(10)
	gib            = uint64(1) << 30
)

// volumeTestFunctions stands in for the rating and account functions. It
// rates like pkg/rf at a fixed unit cost and keeps the balance like pkg/abmf.
// It is stricter than pkg/rf: it fails the test where pkg/rf would cap a
// Price that does not fit Unsigned32.
type volumeTestFunctions struct {
	t             *testing.T
	unitCost      uint32
	ratingErr     error
	noTariff      bool
	balance       int64
	consumedUnits []uint64
	debits        []*charging_datatype.MultipleServicesCreditControl
}

func (f *volumeTestFunctions) rate(
	_ *chf_context.ChfUe, sur *charging_datatype.ServiceUsageRequest,
) (*charging_datatype.ServiceUsageResponse, error) {
	if f.ratingErr != nil {
		return nil, f.ratingErr
	}
	sr := sur.ServiceRating
	rating := &charging_datatype.ServiceRating{
		MonetaryTariff: &charging_datatype.MonetaryTariff{
			RateElement: &charging_datatype.RateElement{
				UnitCost: &charging_datatype.UnitCost{ValueDigits: datatype.Integer64(f.unitCost)},
			},
		},
	}
	if f.noTariff {
		rating.MonetaryTariff = nil
	}
	switch sr.RequestSubType {
	case charging_datatype.REQ_SUBTYPE_DEBIT:
		f.consumedUnits = append(f.consumedUnits, uint64(sr.ConsumedUnits))
		price := uint64(sr.ConsumedUnits) * uint64(f.unitCost)
		require.LessOrEqual(f.t, price, uint64(math.MaxUint32), "Price does not fit Unsigned32")
		rating.Price = datatype.Unsigned32(price)
	case charging_datatype.REQ_SUBTYPE_RESERVE:
		rating.AllowedUnits = sr.MonetaryQuota / datatype.Unsigned32(f.unitCost)
		rating.Price = rating.AllowedUnits * datatype.Unsigned32(f.unitCost)
	}
	return &charging_datatype.ServiceUsageResponse{ServiceRating: rating}, nil
}

func (f *volumeTestFunctions) debit(
	_ *chf_context.ChfUe, ccr *charging_datatype.AccountDebitRequest,
) (*charging_datatype.AccountDebitResponse, error) {
	mscc := ccr.MultipleServicesCreditControl
	f.debits = append(f.debits, mscc)
	rsp := &charging_datatype.AccountDebitResponse{
		MultipleServicesCreditControl: &charging_datatype.MultipleServicesCreditControl{},
	}
	switch {
	case ccr.RequestedAction == charging_datatype.REFUND_ACCOUNT:
		f.balance += int64(mscc.RequestedServiceUnit.CCTotalOctets)
	case mscc.RequestedServiceUnit != nil:
		granted := min(int64(mscc.RequestedServiceUnit.CCTotalOctets), f.balance)
		f.balance -= granted
		rsp.MultipleServicesCreditControl.GrantedServiceUnit = &charging_datatype.GrantedServiceUnit{
			CCTotalOctets: datatype.Unsigned64(granted),
		}
	case mscc.UsedServiceUnit != nil:
		f.balance -= int64(mscc.UsedServiceUnit.CCTotalOctets)
	}
	return rsp, nil
}

func setupVolumeTest(t *testing.T, unitCost uint32, balance int64) (*volumeTestFunctions, *chf_context.ChfUe) {
	t.Helper()
	f := &volumeTestFunctions{t: t, unitCost: unitCost, balance: balance}

	prevRate, prevDebit := sendServiceUsageRequest, sendAccountDebitRequest
	sendServiceUsageRequest, sendAccountDebitRequest = f.rate, f.debit
	self := chf_context.GetSelf()
	prevRatingCfg, prevAbmfCfg := self.RatingCfg, self.AbmfCfg
	self.RatingCfg, self.AbmfCfg = &sm.Settings{}, &sm.Settings{}
	ue := &chf_context.ChfUe{
		Supi:           volumeTestSupi,
		ReservedQuota:  map[int32]int64{},
		UnitCost:       map[int32]uint32{},
		AcctRequestNum: map[int32]uint32{},
		RatingType:     map[int32]charging_datatype.RequestSubType{},
		Cdr:            map[string]*cdrType.CHFRecord{},
	}
	self.UePool.Store(volumeTestSupi, ue)
	t.Cleanup(func() {
		sendServiceUsageRequest, sendAccountDebitRequest = prevRate, prevDebit
		self.RatingCfg, self.AbmfCfg = prevRatingCfg, prevAbmfCfg
		self.UePool.Delete(volumeTestSupi)
	})
	return f, ue
}

func onlineUsage(used uint64, requested *models.Chf_ConvCharging_RequestedUnit,
) models.Chf_ConvCharging_ChargingDataRequest {
	return models.Chf_ConvCharging_ChargingDataRequest{
		SubscriberIdentifier: volumeTestSupi,
		MultipleUnitUsage: []models.Chf_ConvCharging_MultipleUnitUsage{{
			RatingGroup:   volumeTestRG,
			RequestedUnit: requested,
			UsedUnitContainer: []models.Chf_ConvCharging_UsedUnitContainer{{
				QuotaManagementIndicator: models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING,
				TotalVolume:              used,
			}},
		}},
	}
}

func TestMonetaryUnitsSaturates(t *testing.T) {
	require.Equal(t, uint64(1<<40)*1000, monetaryUnits(1<<40, 1000))
	require.Equal(t, uint64(math.MaxInt64), monetaryUnits(math.MaxUint64, 2))
	require.Equal(t, uint64(math.MaxInt64), monetaryUnits(1<<62, 4))
}

// A last report is debited in as many rating requests as the Unsigned32
// Price needs, after one for zero units that reads the unit cost:
// 1.5 GiB at a unit cost of 3 is 4.5 Gi monetary units.
func TestLastUsageAboveThePriceRangeIsDebitedExactly(t *testing.T) {
	f, _ := setupVolumeTest(t, 3, 10_000_000_000)

	sessionChargingReservation(onlineUsage(3*gib/2, nil))

	require.Equal(t, []uint64{0, math.MaxUint32 / 3, 3*gib/2 - math.MaxUint32/3}, f.consumedUnits)
	require.Equal(t, int64(10_000_000_000-9*gib/2), f.balance)
}

func TestLastUsageAboveTwoGiBIsDebitedExactly(t *testing.T) {
	f, _ := setupVolumeTest(t, 1, int64(10*gib))

	sessionChargingReservation(onlineUsage(3*gib, nil))

	require.Equal(t, []uint64{0, 3 * gib}, f.consumedUnits)
	require.Equal(t, int64(7*gib), f.balance)
}

func TestLastUsageAboveFourGiBIsDebitedExactly(t *testing.T) {
	f, _ := setupVolumeTest(t, 1, int64(10*gib))

	sessionChargingReservation(onlineUsage(5*gib, nil))

	require.Equal(t, []uint64{0, math.MaxUint32, 5*gib - math.MaxUint32}, f.consumedUnits)
	require.Equal(t, int64(5*gib), f.balance)
}

// The split uses the unit cost at the time of the debit, not the one kept
// from an earlier reservation.
func TestLastUsageIsSplitByTheCurrentUnitCost(t *testing.T) {
	f, ue := setupVolumeTest(t, 3, 10_000_000_000)
	ue.UnitCost[volumeTestRG] = 1

	sessionChargingReservation(onlineUsage(3*gib/2, nil))

	require.Equal(t, uint32(3), ue.UnitCost[volumeTestRG])
	require.Equal(t, int64(10_000_000_000-9*gib/2), f.balance)
}

// Without an answer from the rating function the debit is not priced at an
// assumed unit cost.
func TestLastUsageIsNotDebitedWithoutARating(t *testing.T) {
	f, _ := setupVolumeTest(t, 3, 10_000_000_000)
	f.ratingErr = errors.New("rating function unreachable")

	sessionChargingReservation(onlineUsage(3*gib/2, nil))

	require.Empty(t, f.debits)
	require.Equal(t, int64(10_000_000_000), f.balance)
}

func TestLastUsageIsNotDebitedWithoutAUnitCost(t *testing.T) {
	f, _ := setupVolumeTest(t, 3, 10_000_000_000)
	f.noTariff = true

	sessionChargingReservation(onlineUsage(3*gib/2, nil))

	require.Empty(t, f.debits)
	require.Equal(t, int64(10_000_000_000), f.balance)
}

// Usage reported with a new request is priced without wrapping, so the
// reservation debits all of it: 1.5 GiB at a unit cost of 3 plus 1000 units
// requested at 3.
func TestUsageAboveThePriceRangeIsReservedExactly(t *testing.T) {
	f, ue := setupVolumeTest(t, 3, 10_000_000_000)
	ue.RatingType[volumeTestRG] = charging_datatype.REQ_SUBTYPE_RESERVE

	info, _ := sessionChargingReservation(onlineUsage(3*gib/2,
		&models.Chf_ConvCharging_RequestedUnit{TotalVolume: 1000}))

	require.Len(t, f.debits, 1)
	require.Equal(t, datatype.Unsigned64(9*gib/2+3000), f.debits[0].RequestedServiceUnit.CCTotalOctets)
	require.Len(t, info, 1)
	require.Equal(t, uint64(1000), info[0].GrantedUnit.TotalVolume)
}

func TestGrantAboveTwoGiBIsNotNarrowed(t *testing.T) {
	_, ue := setupVolumeTest(t, 1, int64(10*gib))
	ue.RatingType[volumeTestRG] = charging_datatype.REQ_SUBTYPE_RESERVE

	info, _ := sessionChargingReservation(onlineUsage(0,
		&models.Chf_ConvCharging_RequestedUnit{TotalVolume: 3 * gib}))

	require.Len(t, info, 1)
	require.Equal(t, 3*gib, info[0].GrantedUnit.TotalVolume)
	require.Equal(t, 3*gib, info[0].GrantedUnit.UplinkVolume)
	require.Equal(t, 3*gib, info[0].GrantedUnit.DownlinkVolume)
}

// A reservation whose price does not fit the Unsigned32 MonetaryQuota is
// capped there, and the grant shrinks with it.
func TestReservationAboveThePriceRangeIsCapped(t *testing.T) {
	f, ue := setupVolumeTest(t, 3, 10_000_000_000)
	ue.RatingType[volumeTestRG] = charging_datatype.REQ_SUBTYPE_RESERVE

	info, _ := sessionChargingReservation(onlineUsage(0,
		&models.Chf_ConvCharging_RequestedUnit{TotalVolume: 2 * gib}))

	require.Len(t, f.debits, 1)
	require.Equal(t, datatype.Unsigned64(math.MaxUint32), f.debits[0].RequestedServiceUnit.CCTotalOctets)
	require.Len(t, info, 1)
	require.Equal(t, uint64(math.MaxUint32/3), info[0].GrantedUnit.TotalVolume)
}
