---
title: DBInstance spec
sidebar_position: 1
---

# DBInstance spec reference

`DBInstance` is a namespaced custom resource in API group `dbaas.opencloud.wso2.com`, version `v1alpha1`, kind `DBInstance`, list kind `DBInstanceList`, singular `dbinstance`, short name `dbi`. The status subresource is enabled.

All child resources (VirtualMachine, volumes, Secrets, Service, Endpoints, ServiceMonitor) are created in the same namespace as the `DBInstance`.

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: dbinstance-sample
spec:
  dbInstanceClass: db.t3.medium
  allocatedStorage: 50
  networkRef: default/vm-net-100
```

## Field support at a glance

| Category | Fields |
| --- | --- |
| Required | `dbInstanceClass`, `allocatedStorage`, `networkRef` |
| Mutable after create | `dbInstanceClass`, `allocatedStorage` (grow only), `running`, `deletionProtection` |
| Immutable after create | `networkRef`, `engineVersion`, `staticNetwork`, `vmPassword`, `dbName`, `masterUsername`, `port`, `storageType`, `credentials` |
| Accepted by the schema but not acted on | `manageMasterUserPassword`, `masterUserPasswordRef`, `multiAZ`, `dbParameterGroupRef`, `tags`, `s3BackupConfig`, `backupRetentionPeriod`, `preferredBackupWindow` |
| Written to the VM but not consumed | `s3BackupConfig` and `backupRetentionPeriod` and `preferredBackupWindow` (see [Backup fields](#backup-fields-not-implemented)) |

:::note How immutability is enforced
Only `networkRef`, `engineVersion`, `staticNetwork` and `vmPassword` carry a CEL `self == oldSelf` rule, so the API server rejects the edit at apply time. `dbName`, `masterUsername`, `port` and `storageType` are compared by the controller after defaults are applied. An edit to one of those is accepted by the API server, but the controller refuses it: `PreflightReady` and `Accepted` become `False` with reason `ImmutableFieldChanged` and the phase becomes `incompatible-parameters`. Revert the change or recreate the instance. See [Status and conditions](/reference/status-and-conditions).
:::

## Sizing

### `spec.dbInstanceClass`

| | |
| --- | --- |
| Type | string |
| Required | yes |
| Validation | `minLength: 1`; must be a key of the instance class catalog (checked by the controller, not the schema) |
| Mutable | yes |

Maps to VM vCPU, memory and the PostgreSQL `max_connections` setting. An unknown value fails preflight with reason `InvalidClass`. Changing the class on a running instance triggers a cold resize (the VM is stopped, resized and restarted); see [Resize and power](/operations/resize-and-power).

Built-in classes (the catalog can be replaced by operator configuration, see [Operator configuration](/configuration/operator-config)):

| Class | vCPU | Memory (MiB) | max_connections |
| --- | --- | --- | --- |
| `db.t3.micro` | 1 | 1024 | 50 |
| `db.t3.small` | 1 | 2048 | 100 |
| `db.t3.medium` | 2 | 4096 | 150 |
| `db.t3.large` | 2 | 8192 | 200 |
| `db.t3.xlarge` | 4 | 16384 | 300 |
| `db.m5.large` | 2 | 8192 | 200 |
| `db.m5.xlarge` | 4 | 16384 | 400 |
| `db.m5.2xlarge` | 8 | 32768 | 600 |
| `db.m5.4xlarge` | 16 | 65536 | 1000 |
| `db.r5.large` | 2 | 16384 | 300 |
| `db.r5.xlarge` | 4 | 32768 | 500 |
| `db.r5.2xlarge` | 8 | 65536 | 800 |

### `spec.allocatedStorage`

| | |
| --- | --- |
| Type | integer (GiB) |
| Required | yes |
| Validation | `minimum: 1`; CEL on update: `self >= oldSelf`, message `allocatedStorage can only grow; shrinking is not supported` |
| Mutable | yes, grow only |

Size of the PostgreSQL data volume. A grow triggers a cold resize. A shrink is rejected by the API server; if one reaches the controller anyway it sets `StorageChangeRejected=True` with reason `UnsupportedShrink`.

### `spec.storageType`

| | |
| --- | --- |
| Type | string |
| Required | no |
| Default | `databaseDefaults.storageClass` from operator configuration (`longhorn` out of the box); applied by the controller, not by the CRD |
| Mutable | no (controller-enforced) |

Name of the StorageClass used for the data volume.

## Engine and database

### `spec.engineVersion`

| | |
| --- | --- |
| Type | string |
| Required | no |
| Default | the default engine version of the baked image revision in use; applied by the controller |
| Validation | CEL `self == oldSelf` (`engineVersion is immutable after creation`) |
| Mutable | no |

PostgreSQL major version, for example `"16"`. Must be one of the versions supported by the resolved baked image, otherwise preflight fails with reason `OSImageInvalid`. Quote the value so YAML does not parse it as a number.

### `spec.dbName`

| | |
| --- | --- |
| Type | string |
| Required | no |
| Default | the `DBInstance` name; applied by the controller |
| Validation | `maxLength: 63`; pattern `^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$` |
| Mutable | no (controller-enforced) |

Name of the initial database created at first boot.

### `spec.masterUsername`

| | |
| --- | --- |
| Type | string |
| Required | no |
| Default | `databaseDefaults.masterUsername` from operator configuration (`dbadmin` out of the box) |
| Validation | `maxLength: 63`; pattern `^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$` |
| Mutable | no (controller-enforced) |

Name of the administrator role. When `spec.credentials` is used, the controller additionally rejects (case-insensitive) the names `postgres`, `postgres_exporter`, `replicator`, `repl`, `public`, `none`, and any name starting with `pg_`. The instance then reports `CredentialsReady=False` with reason `PasswordSourceInvalid`.

### `spec.port`

| | |
| --- | --- |
| Type | integer |
| Required | no |
| Default | `databaseDefaults.port` from operator configuration (`5432` out of the box); applied by the controller |
| Validation | `minimum: 1`, `maximum: 65535` |
| Mutable | no (controller-enforced) |

TCP port PostgreSQL listens on. Reflected in `status.endpoint.port`.

## Credentials

See [Credentials](/security/credentials) for the operational model.

### `spec.credentials`

| | |
| --- | --- |
| Type | object (`CredentialsSpec`) |
| Required | no |
| Default | omitted: the controller generates a 32-character random password |
| Validation | whole-spec CEL, see below |
| Mutable | no |

Selects where the master password comes from. It is a creation-time input: the controller reads the referenced Secret once, keeps its own copy in `pg-<name>-credentials` for retries and repave, and never re-reads it for the password. Editing or deleting the source Secret afterwards does not change the database; it only sets `status.credentials.sourceChanged`.

Two whole-spec CEL rules apply:

| Rule | Message |
| --- | --- |
| `has(self.credentials) == has(oldSelf.credentials) && (!has(self.credentials) \|\| self.credentials == oldSelf.credentials)` | `credentials is immutable after creation` |
| `!has(self.credentials) \|\| (!has(self.masterUserPasswordRef) && !(has(self.manageMasterUserPassword) && self.manageMasterUserPassword))` | `credentials cannot be combined with the reserved manageMasterUserPassword or masterUserPasswordRef fields` |

The first rule is on the whole spec rather than on the field, because a per-field `self == oldSelf` rule is skipped when the field was unset on the old object and would let credentials be added to an instance whose database already has a password.

#### `spec.credentials.passwordSource`

Required object (`PasswordSource`). It currently has one member.

#### `spec.credentials.passwordSource.secretRef`

Required object (`PasswordSecretRef`) pointing at one key of a Secret in the DBInstance's own namespace. DBaaS never modifies or deletes this Secret.

| Field | Type | Required | Validation |
| --- | --- | --- | --- |
| `name` | string | yes | `minLength: 1`, `maxLength: 253`, pattern `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$` |
| `key` | string | yes | `minLength: 1`, `maxLength: 253`, pattern `^[-._a-zA-Z0-9]+$` |

Rules enforced by the controller when it reads the Secret:

- The Secret must exist in the same namespace (`PasswordSourceNotFound` otherwise; the controller polls every 30 seconds).
- The Secret `type` must be exactly `dbaas.opencloud.wso2.com/master-password`.
- The key must exist and the value must be valid UTF-8, contain no NUL byte, contain no CR or LF, and be between 8 and 128 bytes.
- The Secret name must not be `pg-<name>-credentials`, `pg-<name>-connect` or `pg-<name>-cloudinit`, because DBaaS creates and deletes Secrets with those names.

Violations of the last three rules report `CredentialsReady=False` with reason `PasswordSourceInvalid`.

```yaml
spec:
  credentials:
    passwordSource:
      secretRef:
        name: orders-db-password
        key: password
```

### `spec.manageMasterUserPassword` (reserved)

| | |
| --- | --- |
| Type | boolean |
| Default | `false` |

Reserved. The controller never reads it: it generates a random password unless `spec.credentials` is set. It is still accepted in the sample manifest `dbaas_v1alpha1_dbinstance.yaml` but has no effect. Cannot be `true` together with `spec.credentials`.

### `spec.masterUserPasswordRef` (reserved)

| | |
| --- | --- |
| Type | object `{ name: string, key: string }` (both required, no further validation) |

Reserved and never read by the controller. Cannot be set together with `spec.credentials`. Use `spec.credentials.passwordSource.secretRef` instead.

### `spec.vmPassword`

| | |
| --- | --- |
| Type | string |
| Required | no |
| Default | empty (no password login) |
| Validation | CEL `self == oldSelf` (`vmPassword is immutable after creation`) |
| Mutable | no |

Sets a console and SSH password for the `ubuntu` VM user and enables `ssh_pwauth`. For development only. When the operator is configured with `security.rejectVMPassword`, a new instance with a non-empty value fails preflight with reason `VMPasswordNotAllowed` and no VM is created; instances whose VM already exists keep their value. See [Operator configuration](/configuration/operator-config).

## Networking

See [Networking](/networking) for the topology.

### `spec.networkRef`

| | |
| --- | --- |
| Type | string |
| Required | yes |
| Validation | pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?\/[a-z0-9]([-a-z0-9]*[a-z0-9])?$`; CEL `self == oldSelf` (`networkRef is immutable after creation`) |
| Mutable | no |

Harvester Multus NetworkAttachmentDefinition reference in the form `namespace/name`. It is the VM's only network interface: client traffic, package installation during cloud-init and the metrics scrape all use it. The NAD must already exist (the controller does not create networks, and does not currently verify existence) and the VLAN must have internet egress. Recorded in `status.resources.nadName`.

### `spec.staticNetwork`

| | |
| --- | --- |
| Type | object (`NetworkConfig`) |
| Required | no |
| Default | omitted: DHCP on the data NIC |
| Validation | CEL `self == oldSelf` (`staticNetwork is immutable after creation`) |
| Mutable | no |

Configures the data NIC with a static IPv4 address in the cloud-init netplan instead of DHCP.

| Field | Type | Required | Validation |
| --- | --- | --- | --- |
| `address` | string | yes | IPv4 with CIDR prefix, for example `192.168.40.50/24`; pattern `^((25[0-5]\|(2[0-4]\|1\d\|[1-9]\|)\d)\.?\b){4}\/(3[0-2]\|[12]?\d)$` |
| `gateway` | string | yes | IPv4 address; pattern `^((25[0-5]\|(2[0-4]\|1\d\|[1-9]\|)\d)\.?\b){4}$` |
| `nameservers` | array of string | yes | `minItems: 1`; each item matches the IPv4 pattern above |
| `searchDomains` | array of string | no | each item must be a valid DNS label sequence, pattern `^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$` |

```yaml
spec:
  staticNetwork:
    address: 192.168.40.50/24
    gateway: 192.168.40.1
    nameservers: [192.168.40.2]
    searchDomains: [corp.example.com]
```

### `spec.dnsServerIP`

| | |
| --- | --- |
| Type | string |
| Required | no |
| Default | empty: KubeVirt default DNS behaviour |
| Validation | none in the schema |
| Mutable | schema allows edits; the value is applied only when the VM is created |

When set, the VM's resolver is pinned through KubeVirt `dnsPolicy: None` with `dnsConfig.nameservers`. Required on Kube-OVN VPC subnets, where the default would copy an unreachable cluster resolver into the VM. The control plane supplies the per-VPC CoreDNS address. The controller does not guard this field against later edits, and it is not part of `status.appliedSpec`.

## Lifecycle

### `spec.running`

| | |
| --- | --- |
| Type | boolean |
| Required | no |
| Default | `true` (CRD default) |
| Mutable | yes |

`false` stops the VM and preserves storage; `true` starts it. See [Resize and power](/operations/resize-and-power). While the instance is crash-loop halted the controller refuses to start the VM even if `running` is `true`.

### `spec.deletionProtection`

| | |
| --- | --- |
| Type | boolean |
| Required | no |
| Default | `false` |
| Mutable | yes |

While `true`, deleting the `DBInstance` leaves it in place: the finalizer is not removed and `DeletionBlocked=True` (reason `DeletionProtected`) is set. Set it to `false` to let teardown proceed. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).

## Backup fields (not implemented)

No backup tooling consumes these fields in v0.1.0. They are accepted by the schema.

### `spec.backupRetentionPeriod`

Integer, `minimum: 0`, default `0` (disabled). Recorded, but no schedule or retention enforcement exists. When greater than zero and `s3BackupConfig` is set, the S3 values are written into `/etc/dbaas/bootstrap.env` on the VM; otherwise the bootstrap file carries the comment `# backups disabled`.

### `spec.preferredBackupWindow`

String in UTC, pattern `^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$`, for example `02:00-03:00`. Passed through to bootstrap; not consumed.

### `spec.s3BackupConfig`

Object. Only written to the VM's bootstrap file (when `backupRetentionPeriod` is greater than zero); no process uses it.

| Field | Type | Required |
| --- | --- | --- |
| `endpoint` | string | yes |
| `bucket` | string | yes |
| `region` | string | no |
| `secretRef` | string (Secret name with `accessKey` and `secretKey`) | yes |

## Reserved fields (not implemented)

| Field | Type | Notes |
| --- | --- | --- |
| `spec.multiAZ` | boolean | Would enable a standby VM. No standby is created. |
| `spec.dbParameterGroupRef` | string | References a `DBParameterGroup`, a CRD that does not exist in this module. |
| `spec.tags` | map of string to string | Not propagated to child resources or dashboards. |

## Defaults summary

| Field | Applied by | Value |
| --- | --- | --- |
| `spec.running` | CRD | `true` |
| `spec.dbName` | controller | the `DBInstance` name |
| `spec.masterUsername` | controller (`databaseDefaults.masterUsername`) | `dbadmin` |
| `spec.port` | controller (`databaseDefaults.port`) | `5432` |
| `spec.storageType` | controller (`databaseDefaults.storageClass`) | `longhorn` |
| `spec.engineVersion` | controller | default of the baked image revision |
| `spec.credentials` | controller | generated password |

The controller never writes defaults back into `spec`. The effective values are recorded in `status.appliedSpec` once the VM is created.

## Full example

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: orders
  namespace: tenant-a
spec:
  dbInstanceClass: db.m5.large
  allocatedStorage: 100
  engineVersion: "16"
  dbName: orders
  masterUsername: orders_admin
  port: 5432
  storageType: longhorn
  networkRef: default/vm-net-100
  staticNetwork:
    address: 192.168.40.50/24
    gateway: 192.168.40.1
    nameservers: [192.168.40.2]
  credentials:
    passwordSource:
      secretRef:
        name: orders-db-password
        key: password
  running: true
  deletionProtection: true
```

:::info Verified against
- `database/api/v1alpha1/dbinstance_types.go`
- `database/api/v1alpha1/groupversion_info.go`
- `database/config/crd/bases/dbaas.opencloud.wso2.com_dbinstances.yaml`
- `database/internal/ensure/defaults.go`
- `database/internal/ensure/preflight.go`
- `database/internal/ensure/vm.go`
- `database/internal/config/defaults.go`
- `database/internal/credentials/passwordsource.go`
- `database/internal/credentials/cloudinit.go`
- `database/internal/harvester/typed_client.go`
:::
