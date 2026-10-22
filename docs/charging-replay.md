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
- `chf.accountOperations`: archived account replay results.
- `policyData.ues.chargingData`: existing account documents retain their string
  `quota` field; CHF adds `chargingVersion` and recent `chargingOperations`.

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
retention policy with charging clients. Existing accounts need no migration,
but replay guarantees apply to operations first processed by this version.
Independent quota isolation across UPFs is a separate change.

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

The integration tests run the real processor, CDR writer, journal and atomic
account store. The Diameter network boundary and Rating Function are stubbed.
They cover lost replies, context removal/recreation, partial accounting failure,
concurrent duplicate/distinct debits, refunds, archival and unresolved fences.
