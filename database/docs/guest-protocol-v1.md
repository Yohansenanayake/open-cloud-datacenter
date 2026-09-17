# Guest protocol v1 (step 02)

The source contract is [guest.proto](../internal/guestprotocol/v1/guest.proto).
The generated Go types, validation and codecs live in `internal/guestprotocol/v1`.
This step defines messages; it does not register executable handlers, persist
receipts, expose a socket, or publish to RabbitMQ. There is no compatibility path
for the unreleased serial-console protocol.

Documentation uses `automatedBackupConfigVersion` for desired automated backup
settings and `appliedAutomatedBackupConfigVersion` for the installed version.
The code rename is pending: the schema still uses `policy_generation` and
`applied_policy_generation`. Track the agreed naming and related observation
updates in the [design notes](../yohan-docs/backups/new-design/dbaas-guest-protocol.md#automated-backup-configuration-version).

Automated recovery-data cleanup uses `automatedRecoveryDataDeletionVersion`.
Its code rename from `deletion_generation`, including corresponding results and
observations, is also pending; see [DeleteAutomatedRecoveryData](../yohan-docs/backups/new-design/dbaas-guest-protocol.md#6-deleteautomatedrecoverydata).

## Commands and results

| Command | Immutable intent / result |
| --- | --- |
| ValidateRepository | Exact repository identity and versioned encrypted credential; validated binding |
| ApplyBackupPolicy | `automatedBackupConfigVersion`, repository, credential, retention and UTC window; `appliedAutomatedBackupConfigVersion` |
| RunBackup | Required `automatedBackupConfigVersion`, repository, full/differential type and initial/scheduled/catch-up reason; verified artifact |
| CreateSnapshot | Snapshot UID, manual/final type, repository and credential; standalone snapshot artifact |
| DisableAutomatedBackup | New `automatedBackupConfigVersion`; confirmed disabled configuration version |
| DeleteAutomatedRecoveryData | Repository, deleting owner UID, `automatedBackupConfigVersion` and `automatedRecoveryDataDeletionVersion`; completed `automatedRecoveryDataDeletionVersion` |
| AcquireRestoreHold | Hold identity/generation, target UID and frozen PITR source; acknowledged hold generation |
| ReleaseRestoreHold | Same hold identity and authoritative evidence reference; release acknowledgement |
| RestoreDatabase | Frozen source, new target UID, volume UID, credential and hold generation; verified recovery result |
| CancelOperation | Target operation ID; cancellation intent recorded, not proof that processes stopped |
| InspectState | Explicit set of status sections; typed observations |

Repository identity carries repository UID, original source instance UID, HTTPS
endpoint, bucket, effective prefix, region and addressing mode. The command family
determines the automated/manual/final subpath; there is no arbitrary deletion path
or shell command. Admission and handlers must still authorize the binding and
check overlapping prefixes against existing repositories.

Restore intent contains an exact backup label, PostgreSQL major, system identifier
and timeline. Its `oneof` chooses a frozen PITR timestamp or snapshot UID/type.
There is no unresolved “latest” selector. Guest hold commands cover PITR on the
source VM; snapshot holds remain controller-owned and do not require a live source.
Evidence references are identifiers to verify later, not permission to trust an
unverified claim that target reads have stopped.

Retention in ApplyBackupPolicy is 1–35 days. Disabled automation uses its own
command. UTC windows use `HH:MM-HH:MM`, may cross midnight and must be nonempty.
These are protocol checks; cross-resource and physical validation come later.

## Encoding and limits

Use the package's `Encode*` / `Decode*` functions at transport boundaries. Commands
and individual events are binary Protobuf, limited to **65,536 encoded bytes**.
AMQP uses `application/protobuf`. Stable AMQP message/correlation IDs must match
the body IDs when the transport adapter is implemented.

Local request/response envelopes allow **131,072 bytes**, so the IPC wrapper can
carry a maximum-sized command. Pending-event batches contain at most 64 events,
each within the individual event limit, and must also fit the local frame limit.
The outbox reader must stop before that limit and set `has_more`; acknowledgements
are bounded to 64 distinct event IDs. Actual socket framing and peer checks are
step 06.

Decoding rejects empty, malformed, oversized, excessively nested or unknown
messages. Duplicate singular fields and multiple command/result `oneof` values
are rejected before Protobuf can apply last-value-wins decoding. Unknown fields
and enum values are rejected recursively so this implementation never executes
intent it does not understand. Roll out receivers/capabilities before using new
additive fields; incompatible semantics require a protocol major change. Protocol,
task schema and agent software versions are separate.

No field has been removed from this initial schema. When removing a field or enum
value, reserve **both its number and name** in the `.proto`; never reuse them.

## Identity, digest and duplicate admission

The controller freezes a command, including both stable IDs and its operation
deadline, and calls `SealCommand`. Publication retries reuse the same message ID,
operation ID and intent. Resource UIDs prevent a recreated name from satisfying
an old reference.

`IntentDigest` is SHA-256 of the UTF-8 domain prefix
`dbaas.guest.command.intent.v1\n` (ending in a newline) followed by a canonical
JSON projection of the **decoded semantic fields**:

- Objects use decimal Protobuf field numbers as string keys, sorted
  lexicographically by Go's `encoding/json`. Messages project recursively;
  integer and enum values are JSON integers. Default/absent scalar fields are
  omitted. This schema has no maps or floating-point fields.
- Protocol major, instance/resource UIDs, execution deadline and typed command
  payload contribute to the digest. Credential UID and version also contribute.
- Message/operation IDs are compared separately. Issued time, admission expiry,
  digest itself, encryption key ID, encryption algorithm metadata and randomized
  ciphertext are excluded. The only supported encryption algorithm is still
  validated as `age-x25519-v1`.
- Endpoint host case, default HTTPS port and trailing slash are normalized.
  Leading/trailing prefix slashes are removed; empty prefixes, traversal and
  ambiguous interior path segments are rejected. Inspection sections form a
  sorted, duplicate-free set.

This is not a hash of serialized Protobuf bytes. Field ordering, explicit scalar
defaults and randomized encryption must not create a different operation. The
fixed digest vector in the tests protects this v1 definition against accidental
changes. `SealCommand` sets only the digest; normalization does not mutate the
caller's repository data.

The later admission implementation must follow this order:

1. Authenticate the caller and validate the trusted instance/resource binding
   (`ValidateTarget`), then validate the command.
2. Look up both message and operation IDs under transactional uniqueness rules.
   `MatchReceipt` checks both IDs, both UIDs and the digest. Conflicting reuse
   returns `OPERATION_CONFLICT`.
3. Return an existing matching receipt even after expiry. Only for new work,
   call `ValidateNewAdmission` with the current time, check policy/safety/capacity,
   and commit the receipt, delivery intent and admitted event together.

The time checks reject a new admission at or after its admission expiry or overall
deadline, and before its issued time. They never extend an accepted execution
deadline. No in-memory deduplication store is introduced in this step.

## Tasks, events and secrets

Ordinary task payloads contain only task schema version, operation ID and instance
UID. Safety task payloads additionally require a nonzero cleanup generation.
Asynq task type and queue remain separate metadata. The executor will load the
accepted intent from SQLite; Redis receives no credential bundle or configuration.

Admission/status events bind the causing command and carry positive per-operation
sequences. Pre-admission rejections have correlation IDs but sequence zero, and
cannot advance an accepted operation. Hello, health and heartbeat events have no
operation identity/sequence. Sequence persistence, state-transition checks and
event UID/generation authorization remain responsibilities of later store and
controller steps.

Results are typed. A successful restore report requires recovery to have ended,
managed credentials to have been replaced, writability and completed source
reads. Healthy PITR reports require verified bounds, lineage, an initial anchor
and applied policy identity. Schema validation checks the claim's completeness;
handlers must establish these facts from physical evidence. Heartbeats report
liveness only. `AgentHello.capabilities` may be empty; defining commands does not
advertise that their handlers exist.

Credentials carry only an immutable credential UID/version plus age ciphertext.
Use `CommandSummary` for command logs and `NewSafeError` for outgoing errors. These
emit selected identity fields and fixed error text; they never include raw payload,
endpoint credentials, ciphertext or subprocess/SDK error strings. Generated
Protobuf `String()` methods are not redacting loggers. Never log whole messages.
Credential decryption and binding the protected bundle to instance/operation/version
are step 06; the trust model is in [runtime qualification](backup-runtime-qualification.md).

## Generation and verification

```sh
make backup-protobuf-tools       # install the step 01 pinned tools if absent
make generate-guest-protocol     # regenerate tracked guest.pb.go
make check-guest-protocol        # generate into a temp directory and compare
GOTOOLCHAIN=go1.25.7 go test ./internal/guestprotocol/v1
```

Generation uses protoc 33.0 and protoc-gen-go 1.36.10 and checks their versions.
Tests cover every command, result, event and IPC variant; exact size boundaries;
malformed/unknown/ambiguous wire data; protocol and identity mismatches; digest
stability/conflicts; expired duplicates; credential redaction; and minimal task
payloads. Decoder fuzzing exercises malformed inputs and round-trip invariants.
These checks use no Redis, Docker or EC2 broker because they qualify the contract
itself.

Validated on 2026-09-09: protocol tests with Go 1.25.7, reproducible generation,
379,349 decoder fuzz executions with no failure, and `make test` (generation,
vet, unit tests and controller envtest). These results qualify the message
contracts, not durable admission or backup execution.
