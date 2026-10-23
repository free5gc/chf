package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	chargingdb "github.com/free5gc/chf/internal/db"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
)

func (p *Processor) keepChargingLease(record *charging.Record) func() {
	journal, ok := p.journal.(charging.RecoveryJournal)
	if !ok {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(charging.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := journalContext()
				err := journal.RenewLease(ctx, record)
				cancel()
				if err != nil {
					logger.ChargingdataPostLog.Errorf("Charging lease lost for %s: %v", record.ID, err)
					return
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}

// RunChargingRecovery runs one bounded worker, with indexed polling when idle.
// Service shutdown stops new claims and waits for the current invocation.
func (p *Processor) RunChargingRecovery(ctx context.Context) {
	journal, ok := p.journal.(charging.RecoveryJournal)
	if !ok {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		record, err := journal.ClaimRecovery(claimCtx)
		cancel()
		if err == nil && record != nil {
			p.recoverChargingRequest(record)
			continue
		}
		if err != nil {
			logger.ChargingdataPostLog.Errorf("Claim charging recovery: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Processor) recoverChargingRequest(record *charging.Record) {
	stop := p.keepChargingLease(record)
	defer stop()
	if err := p.resumeChargingRequest(record); err != nil {
		ctx, cancel := journalContext()
		defer cancel()
		if retryErr := p.journal.MarkUncertain(ctx, record, err.Error()); retryErr != nil {
			logger.ChargingdataPostLog.Errorf("Schedule charging recovery %s: %v", record.ID, retryErr)
		}
		logger.ChargingdataPostLog.Warnf("Charging recovery %s deferred: %v", record.ID, err)
	}
}

func (p *Processor) resumeChargingRequest(record *charging.Record) error {
	var data models.Chf_ConvCharging_ChargingDataRequest
	if err := json.Unmarshal(record.Request, &data); err != nil {
		return err
	}
	fingerprint, err := charging.Digest(data)
	if err != nil {
		return err
	}
	if record.ID != record.Identity.ID() || fingerprint != record.Fingerprint ||
		data.InvocationSequenceNumber != record.Sequence ||
		(record.Operation != chargingUpdateOperation && record.Operation != chargingReleaseOperation) {
		return fmt.Errorf("invalid persisted charging request identity")
	}
	ue, err := chf_context.GetSelf().NewCHFUe(data.SubscriberIdentifier)
	if err != nil {
		return err
	}
	ue.CULock.Lock()
	defer ue.CULock.Unlock()
	// Discard provisional output and rebuild only the completed event chain.
	if err = p.RestoreCDRs(ue); err != nil {
		return err
	}
	if ue.Cdr[record.Session] == nil || ue.CDRProgress[record.Session].Closed {
		return p.rejectRecoveredRequest(record, missingChargingSession(record.Session))
	}
	if err = p.restoreChargingState(ue, record); err != nil {
		return err
	}
	if problem := validateQuotaOwners(ue, data, record.Session); problem != nil {
		return p.rejectRecoveredRequest(record, problem)
	}
	units, partial, err := p.sessionChargingReservation(data, record.Session, record)
	if err != nil {
		return err
	}
	if record.Operation == chargingReleaseOperation {
		partial = false
	}
	if err = p.applyChargingCDR(ue, record, partial); err != nil {
		return err
	}
	result := charging.Result{Status: http.StatusNoContent}
	if record.Operation == chargingUpdateOperation {
		stamp := record.CreatedAt
		result.Status = http.StatusOK
		result.Body, err = json.Marshal(models.Chf_ConvCharging_ChargingDataResponse{
			InvocationTimeStamp: &stamp, InvocationSequenceNumber: record.Sequence, MultipleUnitInformation: units,
		})
		if err != nil {
			return err
		}
	}
	ctx, cancel := journalContext()
	defer cancel()
	return p.journal.Finish(ctx, record, result, charging.Succeeded)
}

// A validation rejection is safe to resume only before any settlement step.
// Once accounting might have started, preserve the fence for reconciliation.
func (p *Processor) rejectRecoveredRequest(record *charging.Record, problem *models.ProblemDetails) error {
	if record.SettlementPlan != nil && len(record.SettlementPlan.Steps) != 0 {
		return fmt.Errorf("recovery validation failed after settlement began: %s", problem.Detail)
	}
	return p.storeChargingRejection(record, problem)
}

// Both normal processing and recovery execute precisely the frozen business
// request. Replaying ABMF returns its original result; rating is never repeated.
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
	if err := chargingdb.DecodeSnapshot(record.SettlementPlan.Steps[key], &step); err != nil {
		return outcome.UnitInformation, false, err
	}
	if step.UsageIndex != index || step.QuotaKey.ChargingDataRef != record.Session {
		return outcome.UnitInformation, false, charging.ErrConflict
	}
	if body := record.SettlementPlan.Outcomes[key]; len(body) != 0 {
		if err := chargingdb.DecodeSnapshot(body, &outcome); err != nil {
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
