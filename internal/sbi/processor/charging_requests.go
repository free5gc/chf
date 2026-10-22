package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/abmf"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
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

func journalContext() (context.Context, context.CancelFunc) {
	// Client disconnects must not cancel the durable acknowledgement of an
	// accounting operation that may already have changed the balance.
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// beginChargingRequest only claims or looks up an invocation. Each API owns
// its typed response, session validation, accounting and CDR processing.
func (p *Processor) beginChargingRequest(
	data models.Chf_ConvCharging_ChargingDataRequest, ref, operation string,
) (*charging.Record, *models.ProblemDetails) {
	if ref == "" || data.SubscriberIdentifier == "" || data.InvocationSequenceNumber < 0 {
		return nil, &models.ProblemDetails{Status: http.StatusBadRequest, Cause: "MANDATORY_IE_INCORRECT"}
	}
	if p.journal == nil {
		return nil, chargingProblem(fmt.Errorf("charging journal is unavailable"))
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
	ctx, cancel := journalContext()
	record, _, err := p.journal.Begin(ctx, identity)
	cancel()
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

func (p *Processor) finishChargingRequest(
	record *charging.Record, result charging.Result, state string,
) *models.ProblemDetails {
	ctx, cancel := journalContext()
	defer cancel()
	if err := p.journal.Finish(ctx, record, result, state); err != nil {
		return chargingProblem(err)
	}
	return nil
}

// A lookup rejection occurs before any accounting effect and can release the fence.
func (p *Processor) rejectChargingRequest(
	record *charging.Record, problem *models.ProblemDetails,
) *models.ProblemDetails {
	body, err := json.Marshal(problem)
	if err != nil {
		return chargingProblem(err)
	}
	if storageProblem := p.finishChargingRequest(record,
		charging.Result{Status: int(problem.Status), Body: body}, charging.Rejected); storageProblem != nil {
		return storageProblem
	}
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
	ctx, cancel := journalContext()
	defer cancel()
	if err := p.journal.MarkUncertain(ctx, record, detail); err != nil {
		logger.ChargingdataPostLog.Errorf("Persist uncertain charging operation: %v", err)
	}
	return chargingProblem(fmt.Errorf("charging operation requires reconciliation: %s", detail))
}
