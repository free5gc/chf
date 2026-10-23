package charging

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	chargingdb "github.com/free5gc/chf/internal/db"
)

// Quota state and the current invocation's applied-step markers share the
// session fence document, so they can be changed by one atomic write.
type storedQuota struct {
	Key   QuotaKey   `bson:"key"`
	State QuotaState `bson:"state"`
}

type quotaSession struct {
	Active    *string                `bson:"active"`
	Quotas    map[string]storedQuota `bson:"quotas"`
	Applied   map[string]string      `bson:"quotaApplied"`
	Recharges map[string]bool        `bson:"recharges"`
}

func (j *MongoJournal) loadQuotaSession(ctx context.Context, ref string) (*quotaSession, error) {
	var session quotaSession
	if err := j.sessions.FindOne(ctx, bson.M{"_id": ref}).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

func quotaStates(ref string, session *quotaSession) (map[QuotaKey]QuotaState, error) {
	states := make(map[QuotaKey]QuotaState, len(session.Quotas))
	for id, quota := range session.Quotas {
		expectedID, err := Digest(quota.Key)
		if err != nil {
			return nil, err
		}
		if quota.Key.ChargingDataRef != ref || id != expectedID || !quota.State.Known {
			return nil, fmt.Errorf("invalid persisted quota ownership or state")
		}
		states[quota.Key] = quota.State
	}
	return states, nil
}

// LoadQuotas reads durable state without changing it or consuming recharge notices.
func (j *MongoJournal) LoadQuotas(ctx context.Context, ref string) (map[QuotaKey]QuotaState, error) {
	session, err := j.loadQuotaSession(ctx, ref)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return make(map[QuotaKey]QuotaState), nil
	}
	if err != nil {
		return nil, err
	}
	return quotaStates(ref, session)
}

// A fresh invocation already knows its request state and acknowledged snapshot
// bytes. Recovery uses the durable record; public caller-owned snapshots cannot
// substitute for either source.
func (j *MongoJournal) settlementPlan(ctx context.Context, record *Record) (*SettlementPlan, error) {
	stored, plan := record, record.localPlan
	if record.localJournal != j || plan == nil {
		var err error
		stored, err = j.load(ctx, record.ID)
		if err != nil {
			return nil, err
		}
		plan = stored.SettlementPlan
	}
	if stored.Session != record.Session || (stored.State != Processing && stored.State != Uncertain) {
		return nil, ErrPending
	}
	return plan, nil
}

// PrepareQuotas restores one resource before any rating or accounting. Recharge
// notices arriving after a step is frozen remain queued for the next invocation.
func (j *MongoJournal) PrepareQuotas(ctx context.Context, record *Record) (map[QuotaKey]QuotaState, error) {
	for {
		plan, err := j.settlementPlan(ctx, record)
		if err != nil {
			return nil, err
		}
		session, loadErr := j.loadQuotaSession(ctx, record.Session)
		if loadErr != nil {
			return nil, loadErr
		}
		if session.Active == nil || *session.Active != record.ID {
			return nil, ErrPending
		}
		states, stateErr := quotaStates(record.Session, session)
		if stateErr != nil {
			return nil, stateErr
		}
		if len(session.Recharges) == 0 || (plan != nil && len(plan.Steps) != 0) {
			return states, nil
		}
		filter := bson.M{"_id": record.Session, "active": record.ID}
		set := bson.M{}
		unset := bson.M{}
		for group, pending := range session.Recharges {
			filter["recharges."+group] = pending
			unset["recharges."+group] = ""
		}
		for id, quota := range session.Quotas {
			if !quota.State.Closed && session.Recharges[strconv.Itoa(int(quota.Key.RatingGroup))] {
				filter["quotas."+id] = quota
				quota.State.Exhausted = false
				quota.State.RatingType = charging_datatype.REQ_SUBTYPE_RESERVE
				set["quotas."+id] = quota
				states[quota.Key] = quota.State
			}
		}
		update := bson.M{"$unset": unset}
		if len(set) != 0 {
			update["$set"] = set
		}
		result, updateErr := j.sessions.UpdateOne(ctx, filter, update)
		if updateErr != nil {
			return nil, updateErr
		}
		if result.MatchedCount == 1 {
			return states, nil
		}
	}
}

// QueueRecharge does not rewrite a quota underneath a frozen settlement step.
func (j *MongoJournal) QueueRecharge(ctx context.Context, ref string, rg int32) error {
	result, err := j.sessions.UpdateOne(ctx, bson.M{"_id": ref}, bson.M{
		"$set": bson.M{"recharges." + strconv.Itoa(int(rg)): true},
	})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrPending
	}
	return nil
}

// ApplyQuota uses acknowledged immutable snapshots directly on the fresh path,
// and loads them from the journal on recovery. Both paths use the same atomic
// quota update and active-invocation marker checks.
func (j *MongoJournal) ApplyQuota(ctx context.Context, record *Record, index int) (QuotaState, error) {
	plan, err := j.settlementPlan(ctx, record)
	if err != nil {
		return QuotaState{}, err
	}
	if index < 0 || plan == nil {
		return QuotaState{}, ErrConflict
	}
	key := strconv.Itoa(index)
	stepBytes, outcomeBytes := plan.Steps[key], plan.Outcomes[key]
	var step SettlementStep
	var outcome SettlementOutcome
	if err = chargingdb.DecodeSnapshot(stepBytes, &step); err != nil {
		return QuotaState{}, err
	}
	if err = chargingdb.DecodeSnapshot(outcomeBytes, &outcome); err != nil {
		return QuotaState{}, err
	}
	if step.UsageIndex != index || step.QuotaKey != outcome.QuotaKey || !outcome.After.Known ||
		step.QuotaKey.ChargingDataRef != record.Session || step.QuotaKey.RatingGroup != step.RatingGroup ||
		step.QuotaKey.UPFID != step.UPFID {
		return QuotaState{}, ErrConflict
	}
	quotaID, err := Digest(step.QuotaKey)
	if err != nil {
		return QuotaState{}, err
	}
	fingerprint, err := Digest([][]byte{stepBytes, outcomeBytes})
	if err != nil {
		return QuotaState{}, err
	}
	field, marker := "quotas."+quotaID, "quotaApplied."+key
	filter := bson.M{
		"_id": record.Session, "active": record.ID, marker: bson.M{"$exists": false},
	}
	if step.Before.Known {
		filter[field] = storedQuota{Key: step.QuotaKey, State: step.Before}
	} else {
		filter[field] = bson.M{"$exists": false}
	}
	for previous := range plan.Steps {
		previousIndex, parseErr := strconv.Atoi(previous)
		if parseErr != nil {
			return QuotaState{}, parseErr
		}
		if previousIndex < index {
			filter["quotaApplied."+previous] = bson.M{"$exists": true}
		}
	}
	result, err := j.sessions.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		field: storedQuota{Key: step.QuotaKey, State: outcome.After}, marker: fingerprint,
	}})
	if err != nil {
		return QuotaState{}, err
	}
	if result.MatchedCount == 1 {
		return outcome.After, nil
	}
	session, err := j.loadQuotaSession(ctx, record.Session)
	if err != nil {
		return QuotaState{}, err
	}
	if session.Active == nil || *session.Active != record.ID {
		return QuotaState{}, ErrPending
	}
	if session.Applied[key] != fingerprint {
		return QuotaState{}, ErrConflict
	}
	states, err := quotaStates(record.Session, session)
	if err != nil {
		return QuotaState{}, err
	}
	state, exists := states[step.QuotaKey]
	if !exists {
		return QuotaState{}, ErrConflict
	}
	// A later entry in the same invocation may already have advanced this owner.
	// Replaying an earlier entry must never restore its older After state.
	return state, nil
}
