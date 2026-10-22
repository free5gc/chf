# Update and Release replay protection

CHF records Update and Release operations in the configured MongoDB before
running rating, account and CDR processing. The key is the charging resource
reference plus InvocationSequenceNumber. The operation type and request content
must match; only retransmissionIndicator may change. Completed responses are
persisted before sending them, and remain replayable without a live UE/CDR.
Resource references include a UUID to avoid reuse after a CHF restart.

`ChargingDataUpdate` and `ChargingDataRelease` each own their processing flow:
claim/replay, validate the live session, perform accounting/CDR processing, then
persist completion. Shared helpers only manage the journal. There is no generic
execution callback; a fresh Update returns its typed response directly, while a
replayed Update decodes the stored response.

Collections in the configured database:

- `chf.chargingRequests`: original request, fingerprint, processing state and
  completed response. `processing` or `uncertain` operations require reconciliation.
- `chf.chargingSessions`: a fence preventing later invocations on a session from
  bypassing its unresolved operation.
- `chf.accounts`: ABMF-owned authoritative balances, `chargingVersion` and recent
  `chargingOperations`. Its `_id` is the source policy document's immutable `_id`;
  `ueId` and `ratingGroup` are lookup fields, not a permanent account identity.
- `chf.accountOperations`: archived account replay results.
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
subscriber-wide wallet or carry balances automatically into a new policy. An
uncompleted old operation must have its account ID frozen in the future recovery
plan before replay after RG reuse. Looking up the current RG is insufficient.
RF still reads policy rating metadata, so preserving the ABMF account alone does
not recover an entire CHF invocation after policy deletion.

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

## Failure behavior and limits

A completed request whose HTTP reply was lost can be replayed automatically by
the caller, including after CHF restarts. An operation interrupted during
accounting or CDR processing is deliberately fenced, even if ABMF knows its own
part completed. CHF returns 503 rather than repeating partially applied rating,
reservation or CDR changes. Reusing an invocation with changed content/type
returns 409. A session lookup rejection before accounting is stored and replayed
as a rejection, and does not retain the session fence.

Do not clear an unresolved fence or delete its request to retry. Reconciliation
must inspect the original request, account operation results, reservation state
and CDR effects before deciding what remains to be applied. This change does not
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
  migration, policy deletion, RG reuse, concurrency and archived replay.
- `internal/charging/journal_test.go`: persistent request identity and session fences.
- `internal/sbi/processor/charging_requests_test.go`: Update/Release API behavior,
  failure injection and real MongoDB integration.
- `internal/sbi/processor/cdr_test.go`: CDR-specific regressions.
- `internal/abmf/abmf_test.go`: wire answer decoding and identity validation.
- `internal/testutil/mongo.go`: the single disposable database fixture used by tests.

Unit tests with a fake journal keep fast API coverage without MongoDB. Persistent
integration tests additionally run the real processor, CDR writer, journal and atomic
account store. The Diameter network boundary and Rating Function are stubbed.
They cover lost replies, context removal/recreation, partial accounting failure,
concurrent duplicate/distinct debits, refunds, archival and unresolved fences.
