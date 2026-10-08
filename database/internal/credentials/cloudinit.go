/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package credentials

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	operatorconfig "github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/config"
)

// BootstrapParams is the subset of DBInstance spec/class the guest bootstrap
// needs — everything cloud-init bakes into bootstrap.env/netplan. VM shape
// (CPU/mem/image/disks) stays with the Harvester provider and never crosses
// into this package.
type BootstrapParams struct {
	ID             string
	DBName         string
	Port           int
	MasterUser     string
	MaxConnections int
	VMPassword     string
	// StaticNetwork, when non-nil, makes the cloud-init netplan use a
	// static IPv4 config instead of DHCP. Used on VLANs without a DHCP
	// server.
	StaticNetwork *dbaasv1.NetworkConfig
	// EngineVersion is the concrete PostgreSQL major version
	// (internal/ensure's effectiveEngineVersion — never empty) bootstrap.sh
	// activates: a baked image has every catalog-supported version's
	// binaries pre-installed side by side, and bootstrap.sh drops every
	// pre-baked cluster and creates exactly one, for this version, at boot.
	EngineVersion string
	// RestoreID is the UID of the DBRestore that created this instance
	// (spec.restoredFrom.dbRestoreUID), empty for an ordinary instance. When
	// set, the data disk must already hold the restored cluster: bootstrap
	// fails closed rather than formatting or initializing it. The one-time
	// restore work — verify the restored data, hand the DBaaS-managed roles
	// this instance's credentials, and only then open remote access — runs
	// while the disk's own restore marker doesn't yet name this RestoreID,
	// so it happens once per restore, not again on a repave or VM recreation.
	RestoreID string
	// RestoreRecoveryTimeout bounds how long a pending restore waits for
	// PostgreSQL to finish crash-recovering the restored data (operator
	// config restore.recoveryTimeout). Zero means the operator default.
	RestoreRecoveryTimeout time.Duration
}

// BuildCloudInit renders the cloud-init userdata and networkdata that
// KubeVirt's cloudInitNoCloud datasource reads from the ephemeral cloud-init
// Secret (internal/resource.CloudInitSecret).
func BuildCloudInit(p BootstrapParams, m *Material) (userdata, networkdata string) {
	return buildUserData(p, m), BuildNetworkData(p)
}

// BuildNetworkData returns cloud-init network-config v2 for the data NIC.
// The networkdata Secret key configures enp1s0 before network-dependent
// bootstrap steps; a write_files netplan entry would be applied too late.
// DHCP is the default. StaticNetwork supplies address, gateway, and DNS.
// The network must provide egress for any bootstrap package installation.
func BuildNetworkData(p BootstrapParams) string {
	if p.StaticNetwork == nil {
		return `version: 2
ethernets:
  enp1s0:
    dhcp4: true
`
	}
	ns := p.StaticNetwork
	search := ""
	if len(ns.SearchDomains) > 0 {
		search = fmt.Sprintf("\n      search: [%s]", yamlFlowJoin(ns.SearchDomains))
	}
	return fmt.Sprintf(`version: 2
ethernets:
  enp1s0:
    dhcp4: false
    addresses: [%s]
    routes:
      - to: default
        via: %s
    nameservers:
      addresses: [%s]%s
`,
		ns.Address,
		ns.Gateway,
		yamlFlowJoin(ns.Nameservers),
		search,
	)
}

// yamlFlowScalar renders s as a double-quoted YAML scalar, safe to place
// as a flow-sequence item ([a, b, c]) or as a mapping value (key: %s) no
// matter what delimiters, colons, or newlines it contains. Several fields
// reaching this file (Nameservers/SearchDomains items, VMPassword) have no
// CRD pattern constraint, so this is what stops a crafted value from
// altering the cloud-init document's structure. JSON string encoding is
// reused here because every JSON string is already a valid YAML
// double-quoted scalar.
func yamlFlowScalar(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// yamlFlowJoin quotes each item and joins them for a YAML flow sequence.
func yamlFlowJoin(items []string) string {
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = yamlFlowScalar(it)
	}
	return strings.Join(quoted, ", ")
}

// shellSingleQuote makes s safe to substitute into a bash KEY=value
// assignment in bootstrap.env, which bootstrap.sh consumes via `source`
// (a script, not a plain key=value parser) — an unquoted value containing
// shell metacharacters ($, `, ;, |, &, ...) would otherwise execute as root
// on first boot. Embedded CR/LF are flattened first: a raw newline would
// otherwise close the enclosing cloud-init YAML block scalar early and let
// the remainder of the value be parsed as arbitrary YAML/shell content.
func shellSingleQuote(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// restoreRecoveryTimeoutSeconds renders the timeout in whole seconds, rounded
// up so a sub-second remainder is never truncated to an immediate failure.
func restoreRecoveryTimeoutSeconds(d time.Duration) int {
	if d <= 0 {
		d = operatorconfig.Default().Restore.RecoveryTimeout
	}
	return int(math.Ceil(d.Seconds()))
}

func buildUserData(p BootstrapParams, m *Material) string {
	vmUserBlock := ""
	if p.VMPassword != "" {
		vmUserBlock = fmt.Sprintf(`password: %s
chpasswd:
  expire: false
ssh_pwauth: true
`, yamlFlowScalar(p.VMPassword))
	}

	caCertB64 := base64.StdEncoding.EncodeToString([]byte(m.TLS.CACertPEM))
	serverCertB64 := base64.StdEncoding.EncodeToString([]byte(m.TLS.ServerCertPEM))
	serverKeyB64 := base64.StdEncoding.EncodeToString([]byte(m.TLS.ServerKeyPEM))

	// Install everything from bootstrap.sh's apt calls rather than relying
	// on cloud-init's `packages:` module. Minimal cloud images (Ubuntu's
	// `ubuntu-24.04-minimal-cloudimg` is one) strip
	// `package_update_upgrade_install` from their cloud-init module list,
	// so a top-level `packages:` directive is silently ignored. Doing the
	// install from runcmd works on every flavour.
	return fmt.Sprintf(`#cloud-config
%swrite_files:
  - path: /etc/dbaas/bootstrap.env
    permissions: "0600"
    content: |
      INSTANCE_ID=%s
      DB_NAME=%s
      DB_PORT=%d
      MASTER_USER=%s
      MASTER_PASSWORD=%s
      REPL_PASSWORD=%s
      EXPORTER_PASSWORD=%s
      MAX_CONNECTIONS=%d
      ENGINE_VERSION=%s
      RESTORE_ID=%s
      RESTORE_RECOVERY_TIMEOUT_SECONDS=%d
  - path: /etc/ssl/certs/pg-ca.crt
    encoding: b64
    permissions: "0644"
    content: %s
  - path: /etc/ssl/certs/pg-server.crt
    encoding: b64
    permissions: "0644"
    content: %s
  - path: /etc/ssl/private/pg-server.key
    encoding: b64
    permissions: "0600"
    content: %s
  - path: /etc/dbaas/bootstrap.sh
    permissions: "0700"
    content: |
      #!/bin/bash
      set -euo pipefail
      source /etc/dbaas/bootstrap.env

      # A restored instance (RESTORE_ID set) fails closed on anything that
      # doesn't look like the restored cluster: never format, never
      # initialize, never accept remote logins. The reason is left in
      # /var/lib/dbaas/restore-failed for an operator, and the readiness
      # marker is never written, so the instance never reports ready.
      restore_fail() {
        echo "RESTORE FAILED: $1" >&2
        echo "$1" > /var/lib/dbaas/restore-failed
        stop_postgres_fully || true
        shred -uz /etc/dbaas/bootstrap.env 2>/dev/null || rm -f /etc/dbaas/bootstrap.env
        exit 1
      }

      # Stop every PostgreSQL server and wait until none is left. Stopping
      # postgresql.service alone isn't enough: it's an umbrella unit, and
      # systemctl returns while the real cluster unit is still shutting
      # down. A server still shutting down when the data disk is mounted
      # over /var/lib/postgresql writes its shutdown checkpoint's pg_control
      # onto that disk (PostgreSQL writes pg_control through the absolute
      # data-directory path) — which replaced a restored cluster's
      # pg_control in testing. Stopping the instance units by name makes
      # systemctl wait for them; pgrep confirms nothing is left.
      stop_postgres_fully() {
        systemctl stop 'postgresql@*' postgresql || true
        for _ in $(seq 1 "${PG_STOP_TIMEOUT_SECONDS:-60}"); do
          pgrep -x postgres >/dev/null || return 0
          sleep 1
        done
        ! pgrep -x postgres >/dev/null
      }

      # 1. Activate the requested PostgreSQL version. Every catalog-supported
      #    version's binaries are pre-installed in the baked image
      #    (database/images/packer/scripts/provision.sh) — drop whatever
      #    clusters were auto-created at package-install time and create
      #    only the one this tenant actually asked for, so boot has no
      #    dependency on reaching apt repos over the network (the whole
      #    point of baking) and each instance gets a guaranteed-fresh
      #    cluster regardless of image history.
      systemctl enable --now qemu-guest-agent
      # Never drop clusters once /var/lib/postgresql is the mounted,
      # persistent pgdata disk — that would delete live tenant data. Only
      # reset the throwaway clusters apt created on a fresh OS disk: true
      # on a genuine first boot, and right after a repave's disk swap
      # (which resets fstab), since in both cases this path is still the
      # OS disk itself at this point in the script.
      if ! findmnt -n /var/lib/postgresql >/dev/null 2>&1; then
        for ver in $(pg_lsclusters -h | awk '{print $1}' | sort -u); do
          pg_dropcluster --stop "$ver" main 2>/dev/null || true
        done
        pg_createcluster --start "${ENGINE_VERSION}" main
      fi
      PG_VER="${ENGINE_VERSION}"
      PG_CONF="/etc/postgresql/${PG_VER}/main"

      # Move PostgreSQL data onto the dedicated pgdata disk before applying
      # DB-specific configuration. KubeVirt presents the VM's second disk as
      # /dev/vdb based on the VM spec's disk ordering: vda=os, vdb=pgdata,
      # vdc=cloud-init. If the disk is already formatted/mounted, preserve it.
      PGDATA_DEVICE="/dev/vdb"
      PGDATA_MOUNT="/var/lib/postgresql"
      if [ -b "${PGDATA_DEVICE}" ]; then
        if ! stop_postgres_fully; then
          msg="a PostgreSQL server was still running ${PG_STOP_TIMEOUT_SECONDS:-60}s after being stopped; refusing to touch the data disk under it"
          if [ -n "${RESTORE_ID}" ]; then
            restore_fail "${msg}"
          fi
          echo "ERROR: ${msg}" >&2
          exit 1
        fi
        if ! blkid "${PGDATA_DEVICE}" >/dev/null 2>&1; then
          # A restored disk arrives populated from the snapshot; no
          # filesystem means it isn't the disk it should be. Formatting it
          # would turn a failed restore into an empty database.
          if [ -n "${RESTORE_ID}" ]; then
            restore_fail "data disk ${PGDATA_DEVICE} has no filesystem; refusing to format a restored instance's disk"
          fi
          mkfs.ext4 -F -L pgdata "${PGDATA_DEVICE}"
        fi
        PGDATA_UUID=$(blkid -s UUID -o value "${PGDATA_DEVICE}")
        mkdir -p /mnt/dbaas-pgdata
        if ! findmnt -n "${PGDATA_MOUNT}" >/dev/null 2>&1; then
          mount "${PGDATA_DEVICE}" /mnt/dbaas-pgdata
          # Copy the freshly-apt-installed cluster onto vdb on first boot.
          # The "is the disk a virgin cluster?" test is the absence of
          # PostgreSQL's own marker file (PG_VERSION), not "is the dir empty?":
          # mkfs.ext4 always creates lost+found, so a freshly-formatted disk
          # is never literally empty. On reboot the marker exists and we keep
          # the existing data. A restored disk must already hold a cluster of
          # exactly this version — copying a fresh one over it would hide
          # the failure behind an empty database.
          if [ ! -f "/mnt/dbaas-pgdata/${PG_VER}/main/PG_VERSION" ]; then
            if [ -n "${RESTORE_ID}" ]; then
              umount /mnt/dbaas-pgdata
              restore_fail "restored data disk holds no PostgreSQL ${PG_VER} cluster"
            fi
            if [ -d "${PGDATA_MOUNT}/${PG_VER}/main" ]; then
              cp -a "${PGDATA_MOUNT}/." /mnt/dbaas-pgdata/
            fi
          elif [ -n "${RESTORE_ID}" ] && [ "$(cat "/mnt/dbaas-pgdata/${PG_VER}/main/PG_VERSION")" != "${PG_VER}" ]; then
            umount /mnt/dbaas-pgdata
            restore_fail "restored cluster's PG_VERSION does not match engine version ${PG_VER}"
          fi
          umount /mnt/dbaas-pgdata
          if ! grep -q "UUID=${PGDATA_UUID}[[:space:]]${PGDATA_MOUNT}[[:space:]]" /etc/fstab; then
            echo "UUID=${PGDATA_UUID} ${PGDATA_MOUNT} ext4 defaults,nofail 0 2" >> /etc/fstab
          fi
          mount "${PGDATA_MOUNT}"
        fi
        chown -R postgres:postgres "${PGDATA_MOUNT}"
      else
        if [ -n "${RESTORE_ID}" ]; then
          restore_fail "restored data disk ${PGDATA_DEVICE} is not attached"
        fi
        echo "WARN: ${PGDATA_DEVICE} not found; PostgreSQL data remains on the OS disk" >&2
      fi

      # The one-time restore work is pending until the data disk itself
      # records that it was completed for this restore. The marker lives on
      # the data disk, so a repave or VM recreation (fresh OS disk, same
      # data) sees it and doesn't redo the work — and a snapshot taken of a
      # restored instance carries the *old* ID, so restoring it again does.
      RESTORE_MARKER="/var/lib/postgresql/.dbaas-restored-from"
      RESTORE_PENDING=0
      if [ -n "${RESTORE_ID}" ] && [ "$(cat "${RESTORE_MARKER}" 2>/dev/null || true)" != "${RESTORE_ID}" ]; then
        RESTORE_PENDING=1
        # The snapshot was taken from a running server, so the restored data
        # carries the source's postmaster.pid. PostgreSQL refuses to start if
        # any live process on this VM happens to have that PID — a matter of
        # chance on a fresh boot. Nothing can be running on a just-restored
        # disk, so the file is stale by construction (pg_basebackup and
        # pgBackRest exclude it from backups for the same reason).
        rm -f "/var/lib/postgresql/${PG_VER}/main/postmaster.pid"
      fi

      # Fix server key ownership now that postgres user exists
      chown postgres:postgres /etc/ssl/private/pg-server.key

      # Listen on all interfaces and set the port. A pending restore listens
      # on localhost only: the restored catalog still holds the *source's*
      # passwords for the DBaaS-managed roles, so remote logins stay closed
      # until the data is verified and those roles carry this instance's
      # credentials instead.
      LISTEN_ADDRESSES="*"
      if [ "${RESTORE_PENDING}" = "1" ]; then
        LISTEN_ADDRESSES="localhost"
      fi
      sed -i "s/^#\?listen_addresses.*/listen_addresses = '${LISTEN_ADDRESSES}'/" "${PG_CONF}/postgresql.conf"
      sed -i "s/^#\?port.*/port = ${DB_PORT}/" "${PG_CONF}/postgresql.conf"
      sed -i "s/^#\?max_connections.*/max_connections = ${MAX_CONNECTIONS}/" "${PG_CONF}/postgresql.conf"

      # Enable SSL
      sed -i "s/^#\?ssl\b.*/ssl = on/" "${PG_CONF}/postgresql.conf"
      sed -i "s|^#\?ssl_cert_file.*|ssl_cert_file = '/etc/ssl/certs/pg-server.crt'|" "${PG_CONF}/postgresql.conf"
      sed -i "s|^#\?ssl_key_file.*|ssl_key_file = '/etc/ssl/private/pg-server.key'|" "${PG_CONF}/postgresql.conf"
      sed -i "s|^#\?ssl_ca_file.*|ssl_ca_file = '/etc/ssl/certs/pg-ca.crt'|" "${PG_CONF}/postgresql.conf"

      # SSL-only remote connections (hostssl rejects plain-text clients)
      allow_remote_ssl() {
        echo "hostssl all all 0.0.0.0/0 scram-sha-256" >> "${PG_CONF}/pg_hba.conf"
        echo "hostssl replication all 0.0.0.0/0 scram-sha-256" >> "${PG_CONF}/pg_hba.conf"
      }
      if [ "${RESTORE_PENDING}" != "1" ]; then
        allow_remote_ssl
      fi

      bootstrap_fail() {
        if [ "${RESTORE_PENDING}" = "1" ]; then
          restore_fail "$1"
        fi
        echo "ERROR: $1" >&2
        exit 1
      }

      # Restart the cluster unit by name and wait until it accepts
      # connections before anything talks to it. Restarting the postgresql
      # umbrella unit instead returns before the real server is up, so the
      # SQL below would race it. The cluster unit ignores a *slow* start
      # (crash recovery can take arbitrarily long) but reports one that
      # failed outright, so a failed systemctl fails at once; a slow start
      # is waited out for up to $1 seconds.
      restart_postgres() { # restart_postgres <timeout-seconds> <context>
        if ! systemctl restart "postgresql@${PG_VER}-main"; then
          bootstrap_fail "PostgreSQL failed to start $2; see /var/log/postgresql/postgresql-${PG_VER}-main.log"
        fi
        SECONDS=0
        until pg_isready -h 127.0.0.1 -p "${DB_PORT}" >/dev/null 2>&1; do
          if [ "${SECONDS}" -ge "$1" ]; then
            bootstrap_fail "PostgreSQL did not accept connections within $1s $2"
          fi
          sleep 2
        done
      }

      # A pending restore's first start crash-recovers the snapshot's own
      # WAL (Snapshot mode: local recovery only, no recovery.signal), so it
      # gets restore.recoveryTimeout rather than an ordinary start's bound.
      if [ "${RESTORE_PENDING}" = "1" ]; then
        restart_postgres "${RESTORE_RECOVERY_TIMEOUT_SECONDS}" "on the restored data (crash recovery)"
      else
        restart_postgres "${PG_START_TIMEOUT_SECONDS:-300}" "after configuring it"
      fi

      # Verify the restored data is what this restore expected before
      # anything is granted on it.
      if [ "${RESTORE_PENDING}" = "1" ]; then
        [ "$(sudo -u postgres psql -p "${DB_PORT}" -tAc "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'")" = "1" ] \
          || restore_fail "restored cluster has no database ${DB_NAME}"
        [ "$(sudo -u postgres psql -p "${DB_PORT}" -tAc "SELECT 1 FROM pg_roles WHERE rolname = '${MASTER_USER}'")" = "1" ] \
          || restore_fail "restored cluster has no role ${MASTER_USER}"
      fi

      # Create admin user and database. The master user gets CREATEDB and
      # CREATEROLE so it can manage its own databases / roles, but NOT
      # SUPERUSER — RDS-style master users shouldn't be able to bypass
      # the engine's permission system. Database ownership is sufficient
      # for all in-database operations (DDL, GRANT, etc.).
      sudo -u postgres psql -p "${DB_PORT}" <<EOSQL
      DO \$\$
      BEGIN
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${MASTER_USER}') THEN
          CREATE ROLE "${MASTER_USER}" LOGIN CREATEDB CREATEROLE PASSWORD '${MASTER_PASSWORD}';
        END IF;
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'postgres_exporter') THEN
          CREATE ROLE postgres_exporter LOGIN PASSWORD '${EXPORTER_PASSWORD}';
        END IF;
      END \$\$;
      GRANT pg_monitor TO postgres_exporter;
      SELECT 'CREATE DATABASE "${DB_NAME}" OWNER "${MASTER_USER}"'
        WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '${DB_NAME}')\gexec
      EOSQL

      # Finish a pending restore: hand the DBaaS-managed roles this
      # instance's credentials (its Secret is now the only authority — the
      # source's may no longer exist), then open remote access, then record
      # on the data disk that this restore is done. Roles the tenant created
      # are restored data and are left exactly as they were.
      if [ "${RESTORE_PENDING}" = "1" ]; then
        sudo -u postgres psql -v ON_ERROR_STOP=1 -p "${DB_PORT}" <<EOSQL || restore_fail "could not reset the DBaaS-managed roles' credentials"
      ALTER ROLE "${MASTER_USER}" WITH LOGIN PASSWORD '${MASTER_PASSWORD}';
      ALTER ROLE postgres_exporter WITH LOGIN PASSWORD '${EXPORTER_PASSWORD}';
      EOSQL
        sed -i "s/^#\?listen_addresses.*/listen_addresses = '*'/" "${PG_CONF}/postgresql.conf"
        allow_remote_ssl
        restart_postgres "${PG_START_TIMEOUT_SECONDS:-300}" "after opening remote access"
        echo "${RESTORE_ID}" > "${RESTORE_MARKER}"
      fi

      # The baked image leaves PostgreSQL disabled so first boot and repave
      # can prepare the data disk before starting the selected cluster.
      # Once initialization (including any restore) succeeds, enable the
      # umbrella unit for subsequent boots. Its generator starts the selected
      # cluster with start.conf=auto. Do not use --now: the cluster is already
      # running, and its explicit restart/readiness checks above remain the
      # authority for this bootstrap.
      systemctl enable postgresql.service \
        || bootstrap_fail "could not enable PostgreSQL startup on boot"

      # Bootstrap-completion marker, checked by the KubeVirt readiness probe
      # (internal/harvester/typed_client.go). pg_isready alone answers "is a
      # postmaster listening", which goes true several steps earlier — while
      # pg_createcluster's throwaway cluster is up, and again right after the
      # restart above but before the role/database exist. A client that took
      # phase=available at its word in that window got
      # "password authentication failed for user ${MASTER_USER}", because
      # PostgreSQL reports a missing role exactly like a wrong password.
      #
      # Written here, not at the end of the script: the contract is "the
      # master role and its database are usable". Exporter setup below is the
      # separate MonitoringReady axis and must not gate DatabaseReady.
      #
      # Lives on the OS disk, so it is absent on a genuine first boot and
      # after a repave's disk swap (both of which re-run this script), and
      # persists across a plain reboot (which does not). /var/lib/dbaas
      # itself already exists by this point — runcmd below creates and
      # chowns it before bootstrap.sh ever runs.
      touch /var/lib/dbaas/bootstrap-complete

      cat >/etc/default/prometheus-postgres-exporter <<EOEXPORTER
      DATA_SOURCE_NAME=postgresql://postgres_exporter:${EXPORTER_PASSWORD}@127.0.0.1:${DB_PORT}/postgres?sslmode=require
      ARGS="--web.listen-address=:9187"
      EOEXPORTER
      # apt's postinst starts the exporter immediately with its default
      # (DATA_SOURCE_NAME-less) config, so 'systemctl enable --now' here is
      # a no-op against an already-running daemon and the new env file is
      # never read. An explicit restart is what actually picks it up.
      systemctl enable prometheus-postgres-exporter
      systemctl restart prometheus-postgres-exporter

      # Wipe the on-disk copy of the secrets now that PostgreSQL is
      # configured. The K8s Secret stays as the source of truth; leaving
      # bootstrap.env around lets anyone with root inside the VM (or
      # anyone who restores from a snapshot) read the admin password.
      shred -uz /etc/dbaas/bootstrap.env 2>/dev/null || rm -f /etc/dbaas/bootstrap.env
runcmd:
  - mkdir -p /var/lib/dbaas
  - chown root:root /var/lib/dbaas
  - /etc/dbaas/bootstrap.sh
final_message: "DBaaS bootstrap complete for %s"
`,
		vmUserBlock,
		p.ID,
		p.DBName,
		p.Port,
		p.MasterUser,
		m.AdminPassword,
		m.ReplPassword,
		m.ExporterPassword,
		p.MaxConnections,
		shellSingleQuote(p.EngineVersion),
		shellSingleQuote(p.RestoreID),
		restoreRecoveryTimeoutSeconds(p.RestoreRecoveryTimeout),
		caCertB64,
		serverCertB64,
		serverKeyB64,
		p.ID,
	)
}
