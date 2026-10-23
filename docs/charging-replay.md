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
  immutable settlement steps/outcomes, compact CDR replay metadata (`cdrEvent`)
  committed with the completed response, and an immutable request creation time.
  `processing` or
  `uncertain` operations require reconciliation.
- `chf.chargingSessions`: the resource fence, authoritative per-UPF quota state,
  applied-step fingerprints for the active invocation, and pending recharge
  notices. The initial CDR/notification metadata is saved once as `cdrInitial`;
  subsequent CDR reports do not read or rewrite it. Quota changes and their
  applied markers remain atomic. There is no separate CDR archive collection.
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

Startup calls `internal/db.InitializeSchema` to create account and CDR lookup
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
   resulting quota state and response units.
7. Atomically writes that quota state and its applied-step fingerprint to the
   session document, then updates the live quota map. A missing acknowledgement
   leaves the invocation fenced; retrying the saved step cannot apply it twice.

`Planned.Reserved` is the balance after accounting for reported usage. When
`AddGrantedUnits` is true, the actual ABMF grant is added to it to obtain the
outcome; a final grant also changes rating type to DEBIT. Both possible response
branches are frozen before the account call. The outcome is a snapshot; the session
quota and `quotaApplied` marker prove that the quota change has been applied.

Steps are prepared sequentially because repeated entries for the same
resource/RG/UPFID can depend on an earlier entry's actual grant. A crash can therefore leave an
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

## Independent UPF quotas

CHF now follows independently granted UPF quotas. `ChfUe.Quotas` is keyed by
`ChargingDataRef + RatingGroup + UPFID`; each state keeps reserved account units,
rating type, unit cost, `Exhausted` and `Closed`. UPFs and charging resources have
separate state even when their RG is identical. ABMF continues to debit one
shared account, and reserves funds for each UPF separately. This is not the
shared-quota model where SMF redistributes one credit pool across UPFs.

An ONLINE entry without RequestedUnit settles only its owner's reservation,
returns zero granted units and marks that owner Closed in DEBIT mode. An explicit
positive credit request selects RESERVE without requiring request-level FINAL;
it can start a new cycle for a Closed owner. Request-level FINAL cannot switch
another UPF's credit request into settlement. The request journal still prevents
retransmissions from applying an old final report to a newer cycle.

A genuine final grant from ABMF marks only that owner Exhausted and selects
DEBIT. Credit requests on that still-open exhausted owner do not reserve again
until recharge. Recharge clears exhaustion on live matching-RG owners under the
UE lock, preserving Closed owners and other RGs. The notice is persisted first,
and consumed before preparing the next invocation's first settlement step.
Notices for an already-prepared invocation remain queued, without changing its
frozen Before state. No recovery worker is added in this step.

Granted traffic volume is limited by RF's allowance, the requested volume and
that owner's actual remaining reserved funds divided by the next unit cost.
This also applies when ABMF grants fewer account units than requested, or zero;
RF's requested-credit quote alone cannot fund a grant. Zero-cost quotes do not
require monetary backing. Existing quota-threshold response fields are adjusted
to the actual granted volume; a final-grant response keeps its existing trigger
semantics.

An empty UPFID remains supported for a legacy single-UPF resource/RG. Mixing
missing and named UPFIDs in the same online request or while an existing quota is
still open is rejected with HTTP 400 before accounting. Completed legacy owners
can remain as Closed entries while a later cycle uses an explicit UPFID.

Settlement steps and outcomes record the full QuotaKey without a schema-version
field or version-dependent write conditions. Snapshot writes still check resource
ownership and prevent overwriting a saved step or outcome with different content.
Completed HTTP responses can still replay. Reconcile unfinished RG-wide plans
separately and drain live sessions before deploying the new quota representation;
this change does not migrate an old shared balance into separate UPF balances.
Update and Release restore quota state before processing a valid live session.
CDR context/file reconstruction is available separately; automatic recovery of
the complete UE and invocation remains later work.

A request in processing/uncertain state still fences its entire charging
resource. Per-UPF financial state isolation does not let a later invocation
bypass an unresolved request on that resource; progress after failure still
requires the remaining durable-state and recovery work.


## Atomic quota application

The session document keeps `quotas.<quota-key-hash>` entries containing the full
key and authoritative state. `quotaApplied.<usage-index>` stores a fingerprint
of the immutable step and outcome, scoped by `active` to that invocation. No
quota or settlement schema-version field is used.

A fresh invocation retains private immutable snapshot bytes only after their
journal writes are acknowledged. `PrepareQuotas` reads the session once without
reloading that request. `ApplyQuota` reuses the acknowledged step/outcome bytes;
it does not read the growing request document again for each usage entry.
Caller-owned structs or public record fields cannot replace those saved bytes.
The cache is scoped to the journal instance that began the invocation, and is
discarded when finishing or marking it uncertain. Loaded/recovered invocations
read their durable plan instead. No new persistent fields are needed for this cache.

Both paths check ownership, the active resource fence, the expected Before state
and markers for earlier prepared entries. They atomically replace the quota
with After and write its marker. A repeated application verifies the fingerprint
and returns the current durable state, including when a later entry for that
owner has advanced it. It never rolls state back to an earlier After snapshot.

Claiming the next invocation clears the previous marker map in the same write
that acquires `active`, keeping marker storage bounded to one invocation. Quotas
and recharge notices remain. An old invocation cannot apply after its fence has
been released or transferred. Completed invocations continue replaying from the
request journal, whose history is retained independently.

Pending recharge RGs share the session document. Preparing a new invocation
clears exhaustion only on open matching-RG quotas and consumes those notices in
one atomic write, then returns the acknowledged quota state without reading it
back. Notices arriving after a settlement step is prepared wait for
the next invocation. An uncertain invocation therefore retains both its fence
and any pending notice for later reconciliation. The normal API still requires
a live UE/CDR; the recovery helpers can reconstruct CDR state separately, but
this stage does not reconnect Diameter clients or resume whole invocations.


For a fresh invocation with no pending recharge or conflict, quota persistence
adds one session read per invocation and one atomic session write per processed
online usage entry. Earlier request-journal, settlement and account operations
remain. Replay/conflict and recovery paths may perform additional reads. These
are code-path operation counts, not latency or throughput measurements.


## Lightweight CDR replay and recovery

Create saves the initial CDR/notification metadata once before acknowledging its
UUID-based resource. This does not deduplicate repeated Initial requests.
Update/Release use the live CDR projection and its in-memory event cursor. A
released projection rejects new charging requests before any accounting.

Normal CDR processing performs no MongoDB reads or writes of its own. The
original request is already in the journal. Preparing a CDR change operates on
a private copy, then installs the result in memory and regenerates the local file.
The request's event ID prevents applying the same in-flight request twice to that
projection. File replacement uses a temporary file and atomic rename.

The existing successful `Finish` write also saves a small `cdrEvent` object:

- `previous`: the preceding CDR event ID for that resource;
- `partial`: whether this request closes a partial record;
- `stamp`: the fixed event time as RFC3339Nano, preserving its UTC offset;
- `localSequences`: local record numbers allocated by any new segments.

The event is committed atomically with the completed response, with no additional
CDR write. No per-request Before/After CDR snapshots, applied-state documents or
closed-record copies are stored. Each report adds metadata to an existing write,
not more database round trips. Account/quota durability is unchanged. Local file
output remains synchronous in this stage; no background exporter is introduced.

`RestoreCDRs` is an explicit, more expensive reconstruction path. It loads the
initial seed and completed original requests, follows their predecessor links,
and replays usage into a new projection. It does not assume invocation numbers
increase monotonically. Missing/branching history is rejected rather than guessed.
Partial and size-based boundaries, timestamps and local numbers are reproduced
from metadata; replay does not call RF/ABMF or allocate new record numbers. The
rebuilt projection replaces the old one and regenerates the file. History scans
use session/subscriber indexes created by `internal/db.InitializeSchema`.

Only succeeded requests with CDR metadata belong to the completed projection.
Processing, uncertain and rejected requests are excluded. A request interrupted
before completion remains in the original request journal and retains its active
fence; it has not lost its reported usage. A future recovery worker must reconcile
account/quota progress first, reconstruct the completed CDR prefix, then apply the
pending original request once and finish it. An in-memory cursor handles a local
file retry without appending usage again. If the process died before completion,
provisional CDR output is rebuilt from the completed prefix; pending segment local
numbers are fixed when that request eventually completes. This is the deliberate
tradeoff for removing per-report durable CDR snapshots.

The caller holds the UE lock during reconstruction/retry. This stage does not
resume whole invocations automatically, reconnect Diameter clients, or provide
exactly-once CGF/FTP ingestion. Old resources lacking the initial seed or replay
metadata cannot be reconstructed through this path; drain/reconcile them during
rollout. Request history and initial metadata must be retained for reconstruction.

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
to be applied. Durable quota state and atomic applied-step markers now cover
quota application; initial metadata and completed request events cover CDR
reconstruction. A worker must still resume unfinished invocations and
persist their final replies. This change does not reconstruct complete live UE
contexts, deduplicate Initial requests, or enable SMF background retries.
Completed records have no TTL; removing replay history requires a coordinated
retention policy with charging clients. Startup imports history available in
existing account documents; replay guarantees cannot cover earlier operations
that were never recorded. Live quota maps are caches of the persisted resource
state; they are updated only after the quota write is acknowledged.

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
- `internal/charging/journal_test.go`: persistent request identity, session fences,
  immutable snapshots, atomic quota application, ordered replay and queued recharge
  across restarts.
- `internal/sbi/processor/charging_requests_test.go`: shared API fixtures,
  Update/Release replay and rejection behavior, and account failure integration.
- `internal/sbi/processor/charging_quota_test.go`: independent UPF/resource quota
  state, final settlement, funded grants, recharge, UPFID validation, RF/ABMF
  sequencing and settlement storage failures. MongoDB cases verify balances,
  restoring quota caches and lost quota-commit acknowledgements.
- `internal/sbi/processor/cdr_test.go`: initial persistence, zero normal CDR DB
  commands, predecessor-ordered reconstruction, partial/rollover reproduction,
  file failure, lost completion acknowledgement and released-resource validation.
- `internal/abmf/abmf_test.go`: wire answer decoding and identity validation.
- `internal/testutil/mongo.go`: the single disposable database fixture used by tests.

Unit tests with a fake journal keep fast API coverage without MongoDB. Persistent
integration tests additionally run the real processor, CDR writer, journal and atomic
account store. The Diameter network boundary and Rating Function are stubbed.
They cover lost replies, context removal/recreation, partial accounting failure,
concurrent duplicate/distinct debits, refunds, archival, immutable plans, binding/
plan/outcome/quota persistence failures, RF failure before debit, dependent entries
for the same quota owner, UPF/resource isolation, actual funded grants, atomic
quota replay, durable recharge notices and unresolved fences.
