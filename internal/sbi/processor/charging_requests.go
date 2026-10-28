package processor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/abmf"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/rating"
	"github.com/free5gc/openapi/models"
)

const (
	chargingUpdateOperation  = "update"
	chargingReleaseOperation = "release"
)

func chargingProblem(err error) *models.ProblemDetails {
	return &models.ProblemDetails{Status: http.StatusServiceUnavailable, Cause: "CHARGING_FAILED", Detail: err.Error()}
}

func missingChargingSession(ref string) *models.ProblemDetails {
	return &models.ProblemDetails{
		Title: "Context Not Found", Status: http.StatusNotFound,
		Cause: "CONTEXT_NOT_FOUND", Detail: fmt.Sprintf("ChargingDataRef[%s] not found", ref),
	}
}

func (p *Processor) sendAccountDebit(ue *chf_context.ChfUe, request *charging_datatype.AccountDebitRequest) (
	*charging_datatype.AccountDebitResponse, error,
) {
	if p.accountDebit != nil {
		return p.accountDebit(ue, request)
	}
	return abmf.SendAccountDebitRequest(ue, request)
}

func (p *Processor) sendServiceUsage(ue *chf_context.ChfUe, request *charging_datatype.ServiceUsageRequest) (
	*charging_datatype.ServiceUsageResponse, error,
) {
	if p.serviceUsage != nil {
		return p.serviceUsage(ue, request)
	}
	return rating.SendServiceUsageRequest(ue, request)
}

// beginChargingRequest only claims or looks up an invocation. Each API owns
// its typed response, session validation, accounting and CDR processing.
func (p *Processor) beginChargingRequest(
	data models.Chf_ConvCharging_ChargingDataRequest, ref, operation string,
) (*charging.Record, *models.ProblemDetails) {
	if ref == "" || data.SubscriberIdentifier == "" || data.InvocationSequenceNumber < 0 {
		return nil, &models.ProblemDetails{Status: http.StatusBadRequest, Cause: "MANDATORY_IE_INCORRECT"}
	}
	canonical := data
	canonical.RetransmissionIndicator = false
	fingerprint, err := charging.Digest(canonical)
	if err != nil {
		return nil, chargingProblem(err)
	}
	body, err := json.Marshal(canonical)
	if err != nil {
		return nil, chargingProblem(err)
	}
	identity := charging.Identity{
		Request: body, Session: ref, Sequence: data.InvocationSequenceNumber,
		Operation: operation, Fingerprint: fingerprint,
	}
	record, err := p.requests.Begin(identity)
	if err != nil {
		if errors.Is(err, charging.ErrConflict) {
			return nil, &models.ProblemDetails{Status: http.StatusConflict, Cause: "CHARGING_FAILED", Detail: err.Error()}
		}
		return nil, chargingProblem(err)
	}
	if record.State == charging.Rejected {
		var problem models.ProblemDetails
		if err = json.Unmarshal(record.Result.Body, &problem); err != nil {
			return nil, chargingProblem(err)
		}
		return nil, &problem
	}
	return record, nil
}

// A lookup rejection occurs before any accounting effect and can release the fence.
func (p *Processor) rejectChargingRequest(
	record *charging.Record, problem *models.ProblemDetails,
) *models.ProblemDetails {
	body, err := json.Marshal(problem)
	if err != nil {
		return chargingProblem(err)
	}
	p.requests.Finish(record, charging.Result{Status: int(problem.Status), Body: body}, charging.Rejected)
	return problem
}

func (p *Processor) failChargingRequest(
	record *charging.Record, problem *models.ProblemDetails,
) *models.ProblemDetails {
	detail := problem.Detail
	if detail == "" {
		detail = problem.Cause
	}
	if detail == "" {
		detail = http.StatusText(int(problem.Status))
	}
	p.requests.Fail(record)
	return chargingProblem(fmt.Errorf("charging operation awaiting the same invocation retry: %s", detail))
}

func (p *Processor) captureServiceUsage(
	ue *chf_context.ChfUe, request *charging_datatype.ServiceUsageRequest, ratings *[]charging.RatingExchange,
) (*charging_datatype.ServiceUsageResponse, error) {
	response, err := p.sendServiceUsage(ue, request)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("rating response is nil")
	}
	// The caller reuses SUR and tariff pointers. Freeze a deep copy immediately.
	body, err := charging.EncodeSnapshot(charging.RatingExchange{Request: *request, Response: *response})
	if err != nil {
		return nil, err
	}
	var exchange charging.RatingExchange
	if err = charging.DecodeSnapshot(body, &exchange); err != nil {
		return nil, err
	}
	*ratings = append(*ratings, exchange)
	return &exchange.Response, nil
}

func (p *Processor) saveSettlementStep(record *charging.Record, step *charging.SettlementStep) error {
	if record == nil {
		return nil
	}
	body, err := charging.EncodeSnapshot(step)
	if err != nil {
		return err
	}
	record.SettlementPlan.Steps[strconv.Itoa(step.UsageIndex)] = body
	return nil
}

func (p *Processor) saveSettlementOutcome(
	record *charging.Record, index int, outcome *charging.SettlementOutcome,
) error {
	if record == nil {
		return nil
	}
	body, err := charging.EncodeSnapshot(outcome)
	if err != nil {
		return err
	}
	record.SettlementPlan.Outcomes[strconv.Itoa(index)] = body
	return nil
}

// A legacy report without UPFID cannot identify one owner among named UPFs.
// Validate the whole request before any entry changes an account or quota.
func validateQuotaOwners(
	ue *chf_context.ChfUe, data models.Chf_ConvCharging_ChargingDataRequest, ref string,
) *models.ProblemDetails {
	owners := make(map[int32]bool)
	check := func(rg int32, named bool) *models.ProblemDetails {
		if previous, exists := owners[rg]; exists && previous != named {
			return &models.ProblemDetails{
				Status: http.StatusBadRequest, Cause: "MANDATORY_IE_INCORRECT",
				Detail: fmt.Sprintf("UPFID must be consistently present or absent for rating group %d", rg),
			}
		}
		owners[rg] = named
		return nil
	}
	for key, state := range ue.Quotas {
		if key.ChargingDataRef == ref && !state.Closed {
			if problem := check(key.RatingGroup, key.UPFID != ""); problem != nil {
				return problem
			}
		}
	}
	for _, usage := range data.MultipleUnitUsage {
		for _, units := range usage.UsedUnitContainer {
			if units.QuotaManagementIndicator == models.Chf_ConvCharging_QuotaManagementIndicator_ONLINE_CHARGING {
				if problem := check(usage.RatingGroup, usage.UPFID != ""); problem != nil {
					return problem
				}
				break
			}
		}
	}
	return nil
}

func (p *Processor) findChargingUE(supi string, record *charging.Record) (*chf_context.ChfUe, *models.ProblemDetails) {
	ue, ok := chf_context.GetSelf().ChfUeFindBySupi(supi)
	if !ok {
		return nil, p.rejectChargingRequest(record, missingChargingSession(record.Session))
	}
	return ue, nil
}

func (p *Processor) applyQuota(record *charging.Record, index int) (charging.QuotaState, error) {
	var outcome charging.SettlementOutcome
	err := charging.DecodeSnapshot(record.SettlementPlan.Outcomes[strconv.Itoa(index)], &outcome)
	return outcome.After, err
}

func (p *Processor) rechargeQuotas(ue *chf_context.ChfUe, rg int32) (bool, error) {
	if ue.PendingRecharges == nil {
		ue.PendingRecharges = make(map[charging.QuotaKey]bool)
	}
	for key, state := range ue.Quotas {
		if key.RatingGroup == rg && !state.Closed {
			ue.PendingRecharges[key] = true
		}
	}
	return ue.RechargeRatingGroup(rg), nil
}

// A recharge cannot change the result of an already-started invocation. Keep
// its notice in memory until a fresh invocation can use the recharged balance.
func applyPendingRecharges(ue *chf_context.ChfUe, record *charging.Record) {
	if len(record.SettlementPlan.Steps) != 0 {
		return
	}
	for key := range ue.PendingRecharges {
		if key.ChargingDataRef != record.Session {
			continue
		}
		state := ue.Quotas[key]
		if !state.Closed {
			state.Exhausted = false
			state.RatingType = charging_datatype.REQ_SUBTYPE_RESERVE
			ue.Quotas[key] = state
		}
		delete(ue.PendingRecharges, key)
	}
}
