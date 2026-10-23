# Update and Release replay protection

CHF records Update and Release operations in the configured MongoDB before
running rating, account and CDR processing. The key is the charging resource
reference plus InvocationSequenceNumber. The operation type and request content
must match; only retransmissionIndicator may change. Completed responses are
persisted before sending them, and remain replayable without a live UE/CDR.
Resource references include a UUID to avoid reuse after a CHF restart.

`ChargingDataUpdate` and `ChargingDataRelease` each own their processing flow:
claim/replay, validate the live session, rate and persist settlement steps,
perform accounting/CDR processing, then persist completion. Shared helpers manage
journal writes and freeze external rating responses. There is no generic
execution callback; a fresh Update returns its typed response directly, while a
replayed Update decodes the stored response.

Collections in the configured database:

- `chf.chargingRequests`: original request, fingerprint, processing state,
  immutable settlement steps/outcomes and completed response. `processing` or
  `uncertain` operations require reconciliation.
- `chf.chargingSessions`: a fence preventing later invocations on a session from
  bypassing its unresolved operation.
- `chf.accounts`: ABMF-owned authoritative balances, `chargingVersion` and recent
  `chargingOperations`. Its `_id` is the source policy document's immutable `_id`;
  `ueId` and `ratingGroup` are lookup fields, not a permanent account identity.
- `chf.accountOperations`: archived account replay results.
- `chf.accountBindings`: immutable operation-to-account bindings. A binding is
  persisted before its planned debit/refund and survives policy deletion or RG
  reuse, including when the operation has not changed any balance yet.
- `policyData.ues.chargingData`: PCF-owned policy/provisioning data. ABMF reads
  runtime records as initial seeds and never updates their quota or replay history.

## Account lifecycle and rollout

Startup calls `internal/db.InitializeSchema` to create account lookup
indexes, then constructs `Accounts` and imports existing runtime charging records
before accepting traffic. Index creation and shared MongoDB read/write settings live in `internal/db`;
account operations depend on this storage package. Provisioning templates without a rating group
are excluded. Import preserves existing quota, version and recent replay results;
`$setOnInsert` prevents a repeated import from resetting an authoritative balance.
Accounts created by PCF later are imported on first use. A unique sparse operation
index also prevents one recent operation from committing in two account documents.

Stop old ABMF writers and pause policy deletion while switching versions. The
import cannot reconstruct account data already deleted by PCF. Do not run old
and new ABMF versions against the same accounts: they write different balance
collections. A rollback requires explicit balance reconciliation.

PCF may remove its runtime records without deleting `chf.accounts`, the account
archive, or CHF request/session journals. Completed account operations replay
before policy lookup, including after RG reuse. A new debit/refund can use a
retained account after policy deletion when there is exactly one matching
generation; multiple matches fail without changing balances. With a live policy,
its immutable document ID selects the current generation. A running CAS loop
keeps that selected ID even if PCF changes its records concurrently.

This preserves the existing per-policy seed semantics; it does not introduce a
subscriber-wide wallet or carry balances automatically into a new policy. Each
prepared debit/refund now fixes its account ID in `chf.accountBindings` and stores
that binding in the settlement step before execution. ABMF uses this mapping
when applying the operation, instead of looking up the current RG again. New,
unbound operations still need an unambiguous account lookup.

Webconsole currently reads quota from the policy collection. That value is now
an initial seed, not a live balance. Displaying live balances or implementing
recharge requires an ABMF account interface; editing the policy seed must not
silently reset an existing account. No Webconsole or PCF code changes are included.

No replica set is required. ABMF atomically updates the balance, version and
operation result in one document with compare-and-swap. Concurrent duplicate
operations replay the same account answer; distinct operations cannot overwrite
each other's balance changes. Recent results are archived before removal from
the balance document, incrementing the version so archival cannot race a debit.
Majority read/write concerns are used. Storage errors never acknowledge success.

A stable account Session-Id is derived from the charging resource, invocation
sequence and usage-entry index. CC-Request-Number is zero within that individual
account operation. This identity is preserved across transport retries; distinct
UPF entries in one request have different identities.

## Durable settlement steps

Update and Release process usage entries in their original order. For every
online entry that changes quota, CHF now:

1. Reads the current quota state into local variables; live quota is unchanged.
2. Gets all RF results needed for that entry, including all three calls on the
   reservation path, before calling ABMF. Each request/response is copied at once
   so reuse of a request or tariff pointer cannot change earlier quotes.
3. Builds the full account request with its stable Session-Id and request number,
   and persists the operation-to-account binding through the ABMF account store.
4. Persists an immutable `settlementPlan.steps.<usage-index>` snapshot before
   sending the debit/refund. The step includes RG, UPFID, before/planned quota,
   all RF exchanges, account binding/request, the response template and the
   final-grant response branch when applicable.
5. Executes ABMF using that request. Routing metadata is resolved by Diameter
   transport on a copy; it does not mutate the persisted request. Identity and
   financial content remain fixed.
6. Persists `settlementPlan.outcomes.<usage-index>` with the actual account answer,
   resulting quota state and response units, before updating live quota maps.

`Planned.Reserved` is the balance after accounting for reported usage. When
`AddGrantedUnits` is true, the actual ABMF grant is added to it to obtain the
outcome; a final grant also changes rating type to DEBIT. Both possible response
branches are frozen before the account call. The outcome is a snapshot, not an
atomic marker proving that the quota change has been applied.

Steps are prepared sequentially because the next entry may share its RG with
an earlier entry and depend on its actual grant. A crash can therefore leave an
immutable prefix of prepared steps, with later entries not yet prepared. The
original request preserves their order and input. Prepared steps need no new
rating when reconciled; unprepared entries still need rating data. This change
does not guarantee completion of every unfinished invocation after policy data
has been removed, and does not implement a recovery worker.

Snapshots are BSON binary documents using `internal/db.EncodeSnapshot` and
`DecodeSnapshot`. The codec preserves Diameter Time fields, including expiry
and event times, zero values and nanoseconds; ordinary JSON would lose these
fields. Identical snapshots can be written again while the request is processing;
changed snapshots, outcomes without a step, and writes after processing ends are
rejected. Storage failure never lets processing bypass the session fence.

## Failure behavior and limits

A completed request whose HTTP reply was lost can be replayed automatically by
the caller, including after CHF restarts. An operation interrupted during
accounting or CDR processing is deliberately fenced, even if ABMF knows its own
part completed. CHF returns 503 rather than repeating partially applied rating,
reservation or CDR changes. Reusing an invocation with changed content/type
returns 409. A session lookup rejection before accounting is stored and replayed
as a rejection, and does not retain the session fence.

Do not clear an unresolved fence or delete its request to retry. Reconciliation
must inspect the original request, saved settlement steps/outcomes, account
operation results, reservation state and CDR effects before deciding what remains
to be applied. Durable quota state and atomic applied-operation markers are still
needed before a worker can safely restore live quota. This change does not
implement that reconciliation worker, restore live UE/reservation contexts after
restart, deduplicate Initial requests, or enable SMF background retries.
Completed records have no TTL; removing replay history requires a coordinated
retention policy with charging clients. Startup imports history available in
existing account documents; replay guarantees cannot cover earlier operations
that were never recorded. Independent quota isolation across UPFs is a separate
change.

## Tests

Normal unit tests:

```sh
go test ./...
```

Use a disposable MongoDB instance for persistent-store and processor integration
tests. Each test creates and drops only its own UUID-named test database:

```sh
CHF_TEST_MONGO_URI=mongodb://127.0.0.1:27018 go test ./...
CHF_TEST_MONGO_URI=mongodb://127.0.0.1:27018 go test -race ./internal/charging ./internal/sbi/processor ./internal/abmf
```

Tests are grouped by responsibility:

- `internal/charging/accounts_test.go`: arithmetic, atomic account operations,
  migration, policy deletion, RG reuse before the first debit, concurrency and
  archived replay.
- `internal/charging/journal_test.go`: persistent request identity, session fences
  and immutable settlement snapshots, including restart and exact timestamps.
- `internal/sbi/processor/charging_requests_test.go`: Update/Release API behavior,
  failure injection and real MongoDB integration.
- `internal/sbi/processor/cdr_test.go`: CDR-specific regressions.
- `internal/abmf/abmf_test.go`: wire answer decoding and identity validation.
- `internal/testutil/mongo.go`: the single disposable database fixture used by tests.

Unit tests with a fake journal keep fast API coverage without MongoDB. Persistent
integration tests additionally run the real processor, CDR writer, journal and atomic
account store. The Diameter network boundary and Rating Function are stubbed.
They cover lost replies, context removal/recreation, partial accounting failure,
concurrent duplicate/distinct debits, refunds, archival, immutable plans, binding/
plan/outcome persistence failures, RF failure before debit, dependent same-RG
usage entries and unresolved fences.
