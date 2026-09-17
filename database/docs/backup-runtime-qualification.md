# Backup runtime qualification (step 01)

This step selects and tests dependencies. It does not install an agent or executor,
implement command admission, or enable backups. There is no migration from the
unreleased serial-console implementation.

## Reproduce

Run on Linux amd64 with a local Docker daemon that can bind-mount host temporary
directories. Remote Docker daemons are unsupported because Redis uses a Unix
socket shared with the test process. Go downloads and container image pulls need
network access on the first run.

```sh
make test-backup-integration
make backup-protobuf-tools  # requires curl, sha256sum, unzip
```

The integration target builds a temporary Linux amd64 executable with Go 1.25.7,
`CGO_ENABLED=0` and the module checksums, then runs tests without cached results.
The tests are behind `backupintegration`; ordinary unit tests start no services.
Every Redis test owns a disposable container and state directory. RabbitMQ uses
an ephemeral loopback port, test-only credentials and a newly generated TLS key.
Cleanup removes only containers created by the test, including anonymous volumes.
The fixture does not connect to the EC2 broker or use its certificates.

## Version contract

| Component | Pin | Purpose |
| --- | --- | --- |
| Guest Go toolchain | 1.25.7 | Matches the module's declared baseline |
| Asynq | `v0.26.1-0.20260612090333-d135f1439bee` | Exact design-review commit; public APIs only |
| go-redis/v9 | 9.14.1 | Matches the Asynq reference dependency |
| modernc.org/sqlite | 1.54.0 | Pure Go SQLite, compiled into the guest helper |
| RabbitMQ AMQP Go client | 1.3.0 | AMQP **1.0**, including targetless publishing and explicit settlement |
| filippo.io/age | 1.3.2 | X25519 recipient encryption for credential bundles |
| Protobuf Go runtime / generator | 1.36.10 | Same version for generated code and runtime |
| protoc | 33.0 | SHA-256 verified Linux amd64 release archive; Makefile pin |
| Redis | 8.2.9 | Official image pinned by digest in `fixture_test.go` |
| RabbitMQ | 4.3.5 | Official image pinned by digest in `fixture_test.go`; satisfies the design's 4.3+ baseline |
| pgBackRest | 2.59.1 | Guest packaging source baseline; not executed by this fixture |

The Go module graph also raises shared Ginkgo/Gomega, OpenTelemetry and `x/*`
dependencies. Existing Kubernetes compatibility replacements remain in place.

Redis runs without TCP, using AOF, `appendfsync always`,
`no-appendfsync-on-rewrite no`, no periodic RDB snapshots, and `noeviction`.
Guest packaging must place AOF under the verified persistent guest-state mount,
limit socket access to the executor, and set an explicit memory budget. These
container tests do not establish the VM's memory budget or storage guarantees.
SQLite uses WAL, `synchronous=FULL` and a 5-second busy timeout. Production store
initialization must apply these connection settings consistently.

RabbitMQ runs outside the guest. The fixture tests TLS server verification, AMQP
1.0 management, quorum queues, targetless publishing with an accepted outcome,
redelivery and explicit acceptance. Production uses separately provisioned
topology credentials and per-VM permissions; the fixture's administrative user
does not qualify those ACLs, broker HA or reconnection behavior.
Set `tls.Config.ServerName` explicitly to the broker certificate's DNS name or
IP SAN. This client passes `vhost:<name>` as the AMQP host name; leaving TLS
ServerName empty incorrectly uses that virtual-host value for certificate checks.
Keep CA and hostname verification enabled.

For image baking, distribute the pinned Redis build and its license/source
notices; Redis 8 offers AGPLv3, RSALv2 and SSPLv1 licensing options. Record the
selected distribution terms and corresponding source with the image artifacts.
RabbitMQ's official distribution includes MPL-2.0 notices. Preserve dependency
licenses and the complete package manifest in the guest build. Do not silently
replace these pins with the latest distro package.

Use the official [pgBackRest 2.59.1 distribution tarball](https://github.com/pgbackrest/pgbackrest/releases/download/release/2.59.1/pgbackrest-2.59.1.tar.gz),
SHA-256 `1cd522afc33b8ff846ef88c55dc238717c9c8817a4f6ca7c9f64887de9c7402d`.
Step 10 will package it with its MIT license and required native libraries.
pgBackRest is a separate native executable; `CGO_ENABLED=0` applies to our Go
binaries. Run database backup commands as the database OS user; privileged
executor supervision does not require running every pgBackRest command as root.

## Credential encryption and public-key trust

Use the age v1 file format with an X25519 recipient, implemented by `filippo.io/age`.
The protocol algorithm identifier will be `age-x25519-v1`. Encrypt the entire
credential bundle before broker publication; do not implement a custom cipher
or put plaintext secrets into task payloads or SQLite operation records.

The controller provisions one identity per DBInstance UID using the trusted
Kubernetes Secret/bootstrap channel. It records the public recipient and key ID
against that UID before sending credentials. First boot installs private material
for the executor on the verified guest-state volume with restricted ownership.
The unprivileged agent transports ciphertext. Baked images contain no identity.
Repave retains the identity; missing established key material blocks execution.
This trust model includes the controller, Kubernetes Secret access and VM
bootstrap path; it does not protect against compromise of those components.

Never learn or replace a recipient from an unauthenticated broker message.
Rotation requires an authenticated controller update and retention of old keys
until accepted ciphertext has been processed. Bind decrypted contents to the
expected instance UID, operation ID and credential version before use. age
provides recipient confidentiality and ciphertext integrity, not sender identity;
authenticated broker access and command validation remain necessary.
These are implementation decisions for later steps, not bootstrap code added here.

## Evidence and limits

Validated on 2026-09-09: `make test-backup-integration` passed all five tests
(about 92 seconds), `make backup-protobuf-tools` verified both pinned tool versions,
and `make test` passed generation, vet, unit tests and controller envtest. The guest
helper and integration suite used Go 1.25.7; the ordinary controller checks used
the host's Go 1.26.2. Native crash recovery completed in about 64 seconds.

The suite checks TaskID collisions (same and different payloads), pending-task
survival across Redis SIGKILL/restart, persisted retry count/budget/schedule,
and native recovery of an active task after killing its worker and Redis.
It waits for Asynq's real lease recovery, without changing internal Redis keys.
The helper exercises SQLite, age roundtrip/wrong-key/tamper checks and Protobuf
encoding in a static executable; its stdin protocol controls start, child spawn,
blocking and exit. SQLite tests verify committed versus rolled-back rows on reopen.

A process restart is not a power-loss test. SQLite crash recovery, VM reboot and
repave, durable acknowledgement boundaries, subprocess exclusion, broker ACLs,
pgBackRest behavior and full storage-failure qualification belong to later steps.

Version sources: [Asynq reference](https://github.com/hibiken/asynq/tree/d135f1439bee74e989b7f9b41ecd542cc87f024a),
[AMQP client](https://github.com/rabbitmq/rabbitmq-amqp-go-client/releases/tag/v1.3.0),
[RabbitMQ releases](https://www.rabbitmq.com/release-information),
[Redis releases](https://github.com/redis/redis/releases),
[Redis license](https://github.com/redis/redis/blob/8.2.9/LICENSE.txt),
[age](https://github.com/FiloSottile/age/releases/tag/v1.3.2),
[protoc](https://github.com/protocolbuffers/protobuf/releases/tag/v33.0),
[pgBackRest releases](https://pgbackrest.org/release.html).
