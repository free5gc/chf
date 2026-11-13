package processor

import (
	"fmt"
	"strconv"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

// An explicit retry executes the original in-memory account request.
// ABMF returns its cached result if the first response was lost.
func (p *Processor) executeSettlementStep(ue *chf_context.ChfUe, step *charging.SettlementStep) (
	*charging.SettlementOutcome, error,
) {
	after, info := step.Planned, step.UnitInformation
	var answer *charging_datatype.AccountDebitResponse
	if step.AccountRequest != nil {
		var err error
		answer, err = p.sendAccountDebit(ue, step.AccountRequest)
		if err != nil {
			return nil, fmt.Errorf("account operation: %w", err)
		}
		if answer == nil {
			return nil, fmt.Errorf("account response is nil")
		}
		if step.AddGrantedUnits {
			credit := answer.MultipleServicesCreditControl
			if credit == nil || credit.GrantedServiceUnit == nil {
				return nil, fmt.Errorf("account response is missing granted units")
			}
			after.Reserved += int64(credit.GrantedServiceUnit.CCTotalOctets)
			if credit.FinalUnitIndication != nil && credit.FinalUnitIndication.FinalUnitAction == charging_datatype.TERMINATE {
				if step.FinalUnitInformation == nil {
					return nil, fmt.Errorf("missing final-grant response template")
				}
				after.RatingType = charging_datatype.REQ_SUBTYPE_DEBIT
				after.Exhausted = true
				info = *step.FinalUnitInformation
			}
		}
	}
	if info.GrantedUnit != nil && info.GrantedUnit.TotalVolume > 0 && after.UnitCost > 0 {
		funded := uint64(max(after.Reserved, 0)) / uint64(after.UnitCost)
		volume := min(uint64(info.GrantedUnit.TotalVolume), funded)
		info.GrantedUnit = &models.Chf_ConvCharging_GrantedUnit{
			TotalVolume: int32(volume), DownlinkVolume: int32(volume), UplinkVolume: int32(volume),
		}
		if !after.Exhausted && step.AdjustThreshold {
			info.VolumeQuotaThreshold = int32(float32(volume) * step.ThresholdRate)
		}
	}
	return &charging.SettlementOutcome{
		QuotaKey: step.QuotaKey, After: after, AccountResponse: answer, UnitInformation: info,
	}, nil
}

func (p *Processor) resumeSettlementEntry(ue *chf_context.ChfUe, record *charging.Record, index int) (
	models.Chf_ConvCharging_MultipleUnitInformation, bool, error,
) {
	var step charging.SettlementStep
	var outcome charging.SettlementOutcome
	key := strconv.Itoa(index)
	if err := charging.DecodeSnapshot(record.SettlementPlan.Steps[key], &step); err != nil {
		return outcome.UnitInformation, false, err
	}
	if step.UsageIndex != index || step.QuotaKey.ChargingDataRef != record.Session {
		return outcome.UnitInformation, false, charging.ErrConflict
	}
	if body := record.SettlementPlan.Outcomes[key]; len(body) != 0 {
		if err := charging.DecodeSnapshot(body, &outcome); err != nil {
			return outcome.UnitInformation, false, err
		}
	} else {
		computed, err := p.executeSettlementStep(ue, &step)
		if err != nil {
			return outcome.UnitInformation, false, err
		}
		outcome = *computed
		if err = p.saveSettlementOutcome(record, index, &outcome); err != nil {
			return outcome.UnitInformation, false, err
		}
	}
	state, err := p.applyQuota(record, index)
	if err != nil {
		return outcome.UnitInformation, false, err
	}
	ue.Quotas[step.QuotaKey] = state
	return outcome.UnitInformation, step.PartialRecord, nil
}
