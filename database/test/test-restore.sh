#!/usr/bin/env bash
#
# E2E test for DBRestore (Snapshot mode) against a real cluster.
#
# Run it from a machine on the VM network: it needs kubectl access to the
# cluster AND a direct route to the database VMs (psql, and curl for the
# metrics exporter check). The controller under test must already be
# deployed.
#
# What it proves:
#   1. Known data written to a source instance survives snapshot + restore:
#      a row checksum, a second database, and a tenant-created role (with its
#      own password, which restore must leave alone).
#   2. The restore reflects the snapshot, not the live source: rows written
#      after the snapshot are absent on the target.
#   3. The target uses ITS OWN credentials: its Secret logs in, the source's
#      master password does not, and the metrics exporter authenticates.
#   4. Source and target are independent; deleting a Succeeded DBRestore
#      never touches its target, nor the target's disk.
#   5. Deleting the source deletes its disks (data and OS PVCs), and Snapshot
#      mode still works afterwards: the restore reads only the snapshot.
#      Before that (skip with SKIP_HOLD_WAIT=1): deleting the source while a
#      backup of it is running waits for the backup (VM untouched), refuses
#      new snapshots, and finishes once the backup is Ready.
#   6. Controller failure paths: missing snapshot, storage too small, target
#      name conflict, cancellation (deleting an in-progress DBRestore deletes
#      its target), and deleting the DBSnapshot mid-restore (the backend
#      backup survives while the restore holds it). No failed, cancelled or
#      timed-out restore leaves a PVC behind.
#   7. The restore deadline (skip with SKIP_DEADLINE=1): with a short
#      restore.timeout a restore fails as RestoreTimedOut, status.deadline
#      is creation + timeout, and the unfinished target is deleted.
#   8. Observability: restored targets record the snapshot and source they
#      came from (spec.restoredFrom); restores, snapshot refusals and
#      teardown waits are recorded as events (Warning for failures).
#
# Usage: ./test-restore.sh [--cleanup]
#   --cleanup  delete everything this run created on exit — only if the run
#              passed: a failed run always keeps everything, so the evidence
#              survives (the cleanup commands are printed). Deleting the
#              DBInstances deletes their disks; any PVC still left after that
#              is deleted too, as a backstop.
#
# Env (defaults in brackets):
#   NAMESPACE [default]  NETWORK_REF [vm-network-001]  DB_CLASS [db.t3.medium]
#   ALLOCATED_STORAGE [20]  ROWS [20000]  RUN_ID [epoch seconds]
#   VM_PASSWORD [random, printed at start]  console/SSH password for user
#              "ubuntu" on every VM this run creates (source and restore
#              targets), for debugging: virtctl console pg-<instance> -n <ns>
#   PROVISION_TIMEOUT [900]  BACKUP_TIMEOUT [1200]  RESTORE_TIMEOUT [2400]
#   FAIL_TIMEOUT [180]  POLL [5]
#   SKIP_SOURCE_DELETE=1   skip phase 5 (restore after the source is gone)
#   SKIP_HOLD_WAIT=1       in phase 5, delete the source without a backup in
#                          progress (saves one backup, ~2-3 minutes)
#   SKIP_NEGATIVE=1        skip the failure-path checks (phase 6)
#   SKIP_SNAPSHOT_RACE=1   skip deleting the DBSnapshot mid-restore (last step)
#   SKIP_DEADLINE=1        skip the restore-deadline phase (runs by default —
#                          fine on a dev cluster). It RESTARTS THE SHARED
#                          OPERATOR with a short restore.timeout
#                          (DEADLINE_TIMEOUT_SECONDS [120]) and
#                          restore.recoveryTimeout (DEADLINE_RECOVERY_SECONDS
#                          [60]) via env vars, then puts the original
#                          settings back — on exit too, whatever happens.
#                          Refuses to run while any other DBRestore in the
#                          cluster is unfinished (a short timeout would end
#                          it). Needs permission to patch the operator
#                          Deployment: OPERATOR_NS [dbaas-system],
#                          OPERATOR_DEPLOY [dbaas-controller-manager],
#                          OPERATOR_CONTAINER [manager].
#
# Requires: kubectl, psql, base64, GNU date; curl (optional, exporter check).

set -uo pipefail

NAMESPACE="${NAMESPACE:-default}"
NETWORK_REF="${NETWORK_REF:-vm-network-001}"
DB_CLASS="${DB_CLASS:-db.t3.medium}"
ALLOCATED_STORAGE="${ALLOCATED_STORAGE:-20}"
ROWS="${ROWS:-20000}"
RUN_ID="${RUN_ID:-$(date +%s)}"
PROVISION_TIMEOUT="${PROVISION_TIMEOUT:-900}"
BACKUP_TIMEOUT="${BACKUP_TIMEOUT:-1200}"
RESTORE_TIMEOUT="${RESTORE_TIMEOUT:-2400}"
FAIL_TIMEOUT="${FAIL_TIMEOUT:-180}"
POLL="${POLL:-5}"
OPERATOR_NS="${OPERATOR_NS:-dbaas-system}"
OPERATOR_DEPLOY="${OPERATOR_DEPLOY:-dbaas-controller-manager}"
OPERATOR_CONTAINER="${OPERATOR_CONTAINER:-manager}"
DEADLINE_TIMEOUT_SECONDS="${DEADLINE_TIMEOUT_SECONDS:-120}"
DEADLINE_RECOVERY_SECONDS="${DEADLINE_RECOVERY_SECONDS:-60}"
VM_PASSWORD="${VM_PASSWORD:-e2e-$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
# YAML single-quoted scalar: a literal ' is written as ''.
VM_PASSWORD_YAML="'${VM_PASSWORD//\'/\'\'}'"

CLEANUP=false
for arg in "$@"; do
  case "$arg" in
    --cleanup) CLEANUP=true ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

SOURCE="rt-src-$RUN_ID"
SNAPSHOT="rt-snap-$RUN_ID"
# The source deliberately leaves dbName and masterUsername unset — the common
# case, and the one that needs the snapshot to record *effective* values:
# dbName defaults to the source's name made into an identifier
# (DefaultDBName: "rt-src-1" -> "rt_src_1"), and a restore must keep that
# name, not default to the target's.
DB_NAME="${SOURCE//-/_}"
MASTER=""  # read from the source's credentials Secret once provisioned
EXTRA_DB="rt_extra"
TENANT_ROLE="rt_tenant"
TENANT_PW="tp$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
RUN_LABEL="dbaas-e2e/run=$RUN_ID"
LEASE_PREFIX="dbaas-restore-hold-"

CREATED_RESTORES=()  # every DBRestore this run created
CREATED_SNAPSHOTS=("$SNAPSHOT") # every DBSnapshot this run created
CREATED_TARGETS=()   # every target DBInstance a restore was asked to create
CLEANUP_PVCS=()      # PVCs to delete on --cleanup if teardown left any behind
EXPECTED_CHECKSUM=""
SOURCE_MASTER_PW=""
PASS=0
FAIL=0

# ---------- output ----------
say()  { printf '\n\033[1;36m[%(%H:%M:%S)T] == %s ==\033[0m\n' -1 "$*"; }
info() { printf '[%(%H:%M:%S)T] %s\n' -1 "$*"; }
pass() { printf '\033[1;32mPASS\033[0m  %s\n' "$*"; PASS=$((PASS + 1)); }
fail() { printf '\033[1;31mFAIL\033[0m  %s\n' "$*"; FAIL=$((FAIL + 1)); }
die()  { printf '\033[1;31mABORT\033[0m %s\n' "$*"; exit 1; }

check() { # check <description> <want> <got>
  if [[ "$3" == "$2" ]]; then pass "$1"; else fail "$1 (want '$2', got '$3')"; fi
}

require() { command -v "$1" >/dev/null 2>&1 || die "required tool '$1' not found on PATH"; }

# ---------- kubernetes ----------
kc() { kubectl -n "$NAMESPACE" "$@"; }
jp() { kc get "$1" "$2" -o jsonpath="$3" 2>/dev/null; } # jp <kind> <name> <jsonpath>
exists() { kc get "$1" "$2" >/dev/null 2>&1; }          # exists <kind> <name>

# wait_until <description> <timeout-seconds> <command...>
wait_until() {
  local desc="$1" timeout="$2" start
  shift 2
  start=$(date +%s)
  until "$@"; do
    if (( $(date +%s) - start >= timeout )); then
      fail "timed out after ${timeout}s waiting for: $desc"
      return 1
    fi
    sleep "$POLL"
  done
}

not_exists() { ! exists "$1" "$2"; }

# ---------- database ----------
inst_endpoint() { jp dbinstance "$1" '{.status.endpoint.address}'; }
inst_port()     { local p; p=$(jp dbinstance "$1" '{.status.endpoint.port}'); echo "${p:-5432}"; }
inst_cred() { # inst_cred <instance> <admin_user|admin_password>
  kc get secret "pg-$1-credentials" -o jsonpath="{.data.$2}" 2>/dev/null | base64 -d
}

# sql <instance> <user> <password> <db> <query> — prints the result
# (unaligned, tuples only); psql's exit status is returned.
sql() {
  local ep
  ep=$(inst_endpoint "$1")
  [[ -n "$ep" ]] || { echo "no endpoint address on DBInstance $1"; return 2; }
  PGPASSWORD="$3" PGSSLMODE=require PGCONNECT_TIMEOUT=10 \
    psql -h "$ep" -p "$(inst_port "$1")" -U "$2" -d "$4" -v ON_ERROR_STOP=1 -qtAc "$5" 2>&1
}
master_sql() { # master_sql <instance> <db> <query>
  sql "$1" "$(inst_cred "$1" admin_user)" "$(inst_cred "$1" admin_password)" "$2" "$3"
}
master_script() { # master_script <instance> <db>  (SQL on stdin)
  local ep
  ep=$(inst_endpoint "$1")
  PGPASSWORD="$(inst_cred "$1" admin_password)" PGSSLMODE=require PGCONNECT_TIMEOUT=10 \
    psql -h "$ep" -p "$(inst_port "$1")" -U "$(inst_cred "$1" admin_user)" -d "$2" -v ON_ERROR_STOP=1 -q -f - 2>&1
}

login_ok() { [[ "$(master_sql "$1" "$DB_NAME" 'SELECT 1')" == "1" ]]; }

CHECKSUM_SQL="SELECT count(*) || ':' || md5(string_agg(id || '=' || payload, ',' ORDER BY id)) FROM restore_test"

# ---------- waits ----------
wait_available() { # wait_available <instance>
  info "Waiting up to ${PROVISION_TIMEOUT}s for DBInstance/$1 to become available"
  kc wait dbinstance/"$1" --for='jsonpath={.status.phase}=available' --timeout="${PROVISION_TIMEOUT}s" >/dev/null \
    || return 1
  # phase=available means the readiness probe passed; give the first real
  # login a short grace window for boot skew.
  wait_until "a master login to DBInstance/$1" 120 login_ok "$1"
}

snapshot_reason() { jp dbsnapshot "$1" '{.status.conditions[?(@.type=="Ready")].reason}'; }
snapshot_terminal() { # snapshot_terminal [snapshot] — default: the main one
  local r; r=$(snapshot_reason "${1:-$SNAPSHOT}")
  [[ "$r" == "BackupReady" || "$r" == "BackupFailed" ]]
}
snapshot_rejected() { [[ "$(snapshot_reason "$1")" == "$2" ]]; } # <snapshot> <reason>
# snapshot_holds <source-uid> <snapshot> — the source's snapshot hold names it.
snapshot_holds() {
  [[ "$(jp lease "dbaas-snapshot-hold-$1" '{.spec.holderIdentity}')" == "snapshot:$2" ]]
}
deletion_waits_for_snapshot() {
  [[ "$(jp dbinstance "$1" '{.status.conditions[?(@.type=="DeletionBlocked")].reason}')" == "DeletionWaitingForSnapshot" ]]
}

create_snapshot() { # create_snapshot <snapshot> <source>
  [[ " ${CREATED_SNAPSHOTS[*]} " == *" $1 "* ]] || CREATED_SNAPSHOTS+=("$1")
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBSnapshot
metadata:
  name: $1
  namespace: $NAMESPACE
  labels:
    dbaas-e2e/run: "$RUN_ID"
spec:
  sourceInstanceRef:
    name: $2
EOF
}

restore_stage()  { jp dbrestore "$1" '{.status.stage}'; }
restore_reason() { jp dbrestore "$1" '{.status.reason}'; }
restore_terminal() { local s; s=$(restore_stage "$1"); [[ "$s" == "Succeeded" || "$s" == "Failed" ]]; }
restore_lease() { echo "${LEASE_PREFIX}$(jp dbrestore "$1" '{.metadata.uid}')"; }
restore_holds() { exists lease "$(restore_lease "$1")"; }
restore_uid() { jp dbrestore "$1" '{.metadata.uid}'; }

# has_event <kind> <name> <reason> [type] — an event with that reason (and
# type, if given) was recorded on the object. Events live ~1h; a run is shorter.
has_event() {
  local sel="involvedObject.kind=$1,involvedObject.name=$2,reason=$3"
  [[ -n "${4:-}" ]] && sel="$sel,type=$4"
  [[ -n "$(kc get events --field-selector "$sel" -o name 2>/dev/null)" ]]
}

# Disks. Every PVC of an instance is named pg-<instance>-<salt>-..., and a
# restore PVC carries its DBRestore's UID label.
instance_pvcs() { # instance_pvcs <instance>
  kc get pvc -o name 2>/dev/null | sed 's|^persistentvolumeclaim/||' | grep -E "^pg-$1-" || true
}
no_instance_pvcs() { [[ -z "$(instance_pvcs "$1")" ]]; }
restore_pvcs() { # restore_pvcs <restore-uid>
  [[ -n "$1" ]] || return 0
  kc get pvc -l "dbaas.opencloud.wso2.com/restore-uid=$1" -o name 2>/dev/null
}
no_restore_pvcs() { [[ -z "$(restore_pvcs "$1")" ]]; }

wait_restore_terminal() { # wait_restore_terminal <restore> <timeout>
  info "Waiting up to ${2}s for DBRestore/$1 to finish"
  local start last="" now cur
  start=$(date +%s)
  until restore_terminal "$1"; do
    cur="$(restore_stage "$1")/$(restore_reason "$1")"
    [[ "$cur" != "$last" ]] && { info "  DBRestore/$1: $cur"; last="$cur"; }
    now=$(date +%s)
    (( now - start >= $2 )) && { fail "DBRestore/$1 not finished after ${2}s (last: $cur)"; return 1; }
    sleep "$POLL"
  done
  info "  DBRestore/$1: $(restore_stage "$1")/$(restore_reason "$1") — $(jp dbrestore "$1" '{.status.message}')"
}

diagnose_restore() { # diagnose_restore <restore> <target>
  info "---- diagnostics for DBRestore/$1 ----"
  kc get dbrestore "$1" -o jsonpath='{.status}' 2>/dev/null; echo
  if exists dbinstance "$2"; then
    info "target DBInstance/$2 phase=$(jp dbinstance "$2" '{.status.phase}')"
    kc get dbinstance "$2" -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}' 2>/dev/null
    info "If the target never became ready, check the guest: virtctl console pg-$2"
    info "  then: sudo cat /var/lib/dbaas/restore-failed; sudo cloud-init status --long"
  fi
}

# ---------- operator configuration (deadline phase) ----------
OPERATOR_RECONFIGURED=false
declare -A OPERATOR_ENV_BEFORE=() # var -> original value; absent key = was unset
DEADLINE_ENV_VARS=(DBAAS_RESTORE__TIMEOUT DBAAS_RESTORE__RECOVERY_TIMEOUT)

ok() { kubectl -n "$OPERATOR_NS" "$@"; }
operator_container_jsonpath() { echo "{.spec.template.spec.containers[?(@.name=='$OPERATOR_CONTAINER')]$1}"; }

# operator_env_value <var>: prints the value and succeeds if the container
# sets <var> (as a plain value), fails if it doesn't set it at all.
operator_env_value() {
  local names
  names=$(ok get deploy "$OPERATOR_DEPLOY" -o jsonpath="$(operator_container_jsonpath '.env[*].name')" 2>/dev/null)
  grep -qxF "$1" <<<"${names// /$'\n'}" || return 1
  ok get deploy "$OPERATOR_DEPLOY" -o jsonpath="$(operator_container_jsonpath ".env[?(@.name=='$1')].value")"
}

operator_rollout() {
  ok rollout status deploy/"$OPERATOR_DEPLOY" --timeout=300s >/dev/null
}

reconfigure_operator() { # reconfigure_operator <timeout-seconds> <recovery-seconds>
  local v
  for v in "${DEADLINE_ENV_VARS[@]}"; do
    if val=$(operator_env_value "$v"); then OPERATOR_ENV_BEFORE[$v]="$val"; fi
  done
  OPERATOR_RECONFIGURED=true
  ok set env deploy/"$OPERATOR_DEPLOY" -c "$OPERATOR_CONTAINER" \
    "DBAAS_RESTORE__TIMEOUT=${1}s" "DBAAS_RESTORE__RECOVERY_TIMEOUT=${2}s" >/dev/null && operator_rollout
}

# restore_operator puts back exactly what was there before (a value, or no
# variable at all). Idempotent; safe to call from the exit handler.
restore_operator() {
  [[ "$OPERATOR_RECONFIGURED" == "true" ]] || return 0
  local v args=()
  for v in "${DEADLINE_ENV_VARS[@]}"; do
    if [[ -v "OPERATOR_ENV_BEFORE[$v]" ]]; then args+=("$v=${OPERATOR_ENV_BEFORE[$v]}"); else args+=("$v-"); fi
  done
  if ok set env deploy/"$OPERATOR_DEPLOY" -c "$OPERATOR_CONTAINER" "${args[@]}" >/dev/null && operator_rollout; then
    OPERATOR_RECONFIGURED=false
    info "Operator restore settings put back (${args[*]})"
    return 0
  fi
  printf '\033[1;31mOPERATOR NOT RESTORED\033[0m put it back by hand:\n  kubectl -n %s set env deploy/%s -c %s %s\n' \
    "$OPERATOR_NS" "$OPERATOR_DEPLOY" "$OPERATOR_CONTAINER" "${args[*]}"
  return 1
}

# ---------- resources ----------
record_pvcs() { # record_pvcs <instance> — remember its disks for --cleanup
  local d o
  d=$(jp dbinstance "$1" '{.status.resources.dataVolumeName}')
  o=$(jp dbinstance "$1" '{.status.resources.osDiskPVCName}')
  [[ -n "$d" ]] && CLEANUP_PVCS+=("$d")
  [[ -n "$o" ]] && CLEANUP_PVCS+=("$o")
  return 0
}

create_restore() { # create_restore <restore> <target> <snapshot> <storage>
  CREATED_RESTORES+=("$1")
  CREATED_TARGETS+=("$2")
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBRestore
metadata:
  name: $1
  namespace: $NAMESPACE
  labels:
    dbaas-e2e/run: "$RUN_ID"
spec:
  snapshotRef:
    name: $3
  targetInstanceName: $2
  dbInstanceClass: $DB_CLASS
  networkRef: $NETWORK_REF
  allocatedStorage: $4
  vmPassword: $VM_PASSWORD_YAML
EOF
}

on_exit() {
  local code=$?
  restore_operator || code=1
  printf '\n\033[1m%d passed, %d failed\033[0m (run %s)\n' "$PASS" "$FAIL" "$RUN_ID"
  if [[ "$CLEANUP" != "true" || "$code" != "0" || "$FAIL" != "0" ]]; then
    [[ "$CLEANUP" == "true" ]] && info "Run failed — keeping every resource for inspection despite --cleanup"
    info "Leaving test resources in place. To remove them later:"
    info "  kubectl -n $NAMESPACE delete dbrestore -l $RUN_LABEL"
    info "  kubectl -n $NAMESPACE delete dbinstance ${CREATED_TARGETS[*]:-} $SOURCE --ignore-not-found"
    info "  kubectl -n $NAMESPACE delete dbsnapshot ${CREATED_SNAPSHOTS[*]} --ignore-not-found"
    info "  then check no disks are left: kubectl -n $NAMESPACE get pvc | grep -E 'rt-(src|tgt)'"
    exit "$code"
  fi
  say "Cleaning up"
  local r t uid
  for t in "${CREATED_TARGETS[@]}" "$SOURCE"; do
    exists dbinstance "$t" && record_pvcs "$t"
    mapfile -t -O "${#CLEANUP_PVCS[@]}" CLEANUP_PVCS < <(
      kc get pvc -o name 2>/dev/null | sed 's|^persistentvolumeclaim/||' | grep -E "^pg-${t}-" || true)
  done
  for r in "${CREATED_RESTORES[@]}"; do
    uid=$(jp dbrestore "$r" '{.metadata.uid}')
    [[ -n "$uid" ]] && mapfile -t -O "${#CLEANUP_PVCS[@]}" CLEANUP_PVCS < <(
      kc get pvc -l "dbaas.opencloud.wso2.com/restore-uid=$uid" -o name 2>/dev/null | sed 's|^persistentvolumeclaim/||')
  done
  # DBRestores first (an in-progress one cancels its own target), then the
  # instances (which delete their own disks), then the snapshot, then any
  # disk still left as a backstop.
  kc delete dbrestore -l "$RUN_LABEL" --ignore-not-found --timeout=300s
  for t in "${CREATED_TARGETS[@]}" "$SOURCE"; do
    kc delete dbinstance "$t" --ignore-not-found --timeout=600s
  done
  for t in "${CREATED_SNAPSHOTS[@]}"; do
    kc delete dbsnapshot "$t" --ignore-not-found --timeout=600s
  done
  for t in $(printf '%s\n' "${CLEANUP_PVCS[@]}" | sort -u); do
    kc delete pvc "$t" --ignore-not-found --timeout=120s
  done
  exit "$code"
}
trap on_exit EXIT

# =====================================================================
# Phase 1 — source instance with known data, and a snapshot of it
# =====================================================================
phase_seed() {
  say "Phase 1: provision DBInstance/$SOURCE and seed known data"
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: $SOURCE
  namespace: $NAMESPACE
  labels:
    dbaas-e2e/run: "$RUN_ID"
spec:
  dbInstanceClass: $DB_CLASS
  allocatedStorage: $ALLOCATED_STORAGE
  networkRef: $NETWORK_REF
  vmPassword: $VM_PASSWORD_YAML
  backup: {}
EOF
  wait_available "$SOURCE" || die "source DBInstance/$SOURCE never became usable"
  SOURCE_MASTER_PW=$(inst_cred "$SOURCE" admin_password)
  MASTER=$(inst_cred "$SOURCE" admin_user)

  local out
  out=$(master_script "$SOURCE" "$DB_NAME" <<EOF
CREATE TABLE restore_test (id int PRIMARY KEY, payload text NOT NULL);
INSERT INTO restore_test SELECT g, md5(g::text) FROM generate_series(1, $ROWS) g;
CREATE TABLE restore_marker (label text PRIMARY KEY);
INSERT INTO restore_marker VALUES ('before-snapshot');
CREATE ROLE $TENANT_ROLE LOGIN PASSWORD '$TENANT_PW';
GRANT SELECT ON restore_test TO $TENANT_ROLE;
CREATE DATABASE $EXTRA_DB;
EOF
  ) || die "seeding the source failed: $out"
  out=$(master_script "$SOURCE" "$EXTRA_DB" <<EOF
CREATE TABLE extra (v text);
INSERT INTO extra VALUES ('extra-ok');
EOF
  ) || die "seeding $EXTRA_DB failed: $out"

  EXPECTED_CHECKSUM=$(master_sql "$SOURCE" "$DB_NAME" "$CHECKSUM_SQL")
  [[ "$EXPECTED_CHECKSUM" == "$ROWS:"* ]] || die "unexpected source checksum: $EXPECTED_CHECKSUM"
  info "Seeded $ROWS rows; checksum $EXPECTED_CHECKSUM"

  say "Phase 1: snapshot DBSnapshot/$SNAPSHOT"
  create_snapshot "$SNAPSHOT" "$SOURCE"
  info "Waiting up to ${BACKUP_TIMEOUT}s for the backup to finish"
  wait_until "DBSnapshot/$SNAPSHOT to finish" "$BACKUP_TIMEOUT" snapshot_terminal || die "snapshot never finished"
  [[ "$(jp dbsnapshot "$SNAPSHOT" '{.status.conditions[?(@.type=="Ready")].reason}')" == "BackupReady" ]] \
    || die "snapshot failed: $(jp dbsnapshot "$SNAPSHOT" '{.status.conditions[?(@.type=="Ready")].message}')"
  pass "DBSnapshot/$SNAPSHOT is Ready"

  # Written strictly after the snapshot: a restore must NOT contain these.
  out=$(master_script "$SOURCE" "$DB_NAME" <<EOF
INSERT INTO restore_marker VALUES ('after-snapshot');
UPDATE restore_test SET payload = 'changed-after-snapshot' WHERE id = 1;
EOF
  ) || die "post-snapshot write failed: $out"
  [[ "$(master_sql "$SOURCE" "$DB_NAME" "$CHECKSUM_SQL")" != "$EXPECTED_CHECKSUM" ]] \
    || die "post-snapshot write did not change the source checksum — the test can't tell snapshot from live data"
  info "Wrote post-snapshot changes to the source"
}

# =====================================================================
# Verifying a restored target
# =====================================================================
exporter_up() { curl -s --max-time 10 "http://$(inst_endpoint "$1"):9187/metrics" 2>/dev/null | grep -q '^pg_up 1'; }

verify_target() { # verify_target <restore> <target>
  local restore="$1" target="$2" got target_pw

  check "DBRestore/$restore succeeded" "Succeeded" "$(restore_stage "$restore")"
  if [[ "$(restore_stage "$restore")" != "Succeeded" ]]; then
    diagnose_restore "$restore" "$target"
    return 1
  fi
  wait_until "a master login to DBInstance/$target with its own credentials" 120 login_ok "$target" \
    || { diagnose_restore "$restore" "$target"; return 1; }
  pass "target logs in with its own credentials Secret"

  check "target inherited the source's effective dbName (not its own name)" "$DB_NAME" \
    "$(jp dbinstance "$target" '{.spec.dbName}')"
  check "target inherited the source's effective masterUsername" "$MASTER" "$(inst_cred "$target" admin_user)"
  check "no database named after the target was created" "0" \
    "$(master_sql "$target" "$DB_NAME" "SELECT count(*) FROM pg_database WHERE datname = '${target//-/_}'")"
  check "row checksum matches the snapshot" "$EXPECTED_CHECKSUM" "$(master_sql "$target" "$DB_NAME" "$CHECKSUM_SQL")"
  check "only pre-snapshot markers present (restore reflects the snapshot, not the live source)" \
    "before-snapshot" "$(master_sql "$target" "$DB_NAME" "SELECT string_agg(label, ',' ORDER BY label) FROM restore_marker")"
  check "second database restored" "extra-ok" "$(master_sql "$target" "$EXTRA_DB" 'SELECT v FROM extra')"
  check "tenant role restored with its own password and grants" \
    "$ROWS" "$(sql "$target" "$TENANT_ROLE" "$TENANT_PW" "$DB_NAME" 'SELECT count(*) FROM restore_test')"

  target_pw=$(inst_cred "$target" admin_password)
  if [[ "$target_pw" == "$SOURCE_MASTER_PW" ]]; then
    fail "target and source have the same master password — the credential-reset check is meaningless"
  else
    got=$(sql "$target" "$MASTER" "$SOURCE_MASTER_PW" "$DB_NAME" 'SELECT 1')
    if [[ "$got" == "1" ]]; then
      fail "the SOURCE's master password still logs in to the target — credentials were not reset"
    else
      pass "the source's master password is rejected by the target"
    fi
  fi

  if command -v curl >/dev/null 2>&1; then
    wait_until "the target's metrics exporter to report pg_up 1" 180 exporter_up "$target" \
      && pass "metrics exporter authenticates with the target's credentials (pg_up 1)"
  else
    info "curl not found — skipping the exporter check"
  fi

  got=$(jp dbinstance "$target" '{.status.resources.dataVolumeName}')
  [[ "$got" == "pg-${target}-restore-"* ]] && pass "target runs on the restore PVC ($got)" \
    || fail "target data volume '$got' is not the restore PVC (want pg-${target}-restore-*)"
  check "target spec.restoredFrom names this DBRestore" \
    "$(jp dbrestore "$restore" '{.metadata.uid}')" "$(jp dbinstance "$target" '{.spec.restoredFrom.dbRestoreUID}')"
  check "target spec.restoredFrom records the snapshot and source it came from" \
    "$SNAPSHOT/$(jp dbsnapshot "$SNAPSHOT" '{.metadata.uid}')/$SOURCE" \
    "$(jp dbinstance "$target" '{.spec.restoredFrom.dbSnapshotName}/{.spec.restoredFrom.dbSnapshotUID}/{.spec.restoredFrom.sourceInstanceName}')"
  local ev missing=""
  for ev in VolumeRestoring TargetStarting Succeeded; do
    has_event DBRestore "$restore" "$ev" Normal || missing="$missing $ev"
  done
  [[ -z "$missing" ]] && pass "DBRestore/$restore recorded its transitions as events" \
    || fail "DBRestore/$restore is missing events:$missing"
  check "DBRestore status records the target's UID" \
    "$(jp dbinstance "$target" '{.metadata.uid}')" "$(jp dbrestore "$restore" '{.status.targetInstanceUID}')"
  check "DBRestore status records the snapshot's UID" \
    "$(jp dbsnapshot "$SNAPSHOT" '{.metadata.uid}')" "$(jp dbrestore "$restore" '{.status.snapshotUID}')"
  not_exists lease "$(restore_lease "$restore")" && pass "restore hold released after success" \
    || fail "restore hold Lease $(restore_lease "$restore") still exists after success"
}

# =====================================================================
# Phase 2–4 — restore while the source exists
# =====================================================================
phase_restore_live_source() {
  local restore="rt-restore1-$RUN_ID" target="rt-tgt1-$RUN_ID"
  say "Phase 2: restore DBSnapshot/$SNAPSHOT into DBInstance/$target (source still running)"
  create_restore "$restore" "$target" "$SNAPSHOT" "$ALLOCATED_STORAGE"
  wait_restore_terminal "$restore" "$RESTORE_TIMEOUT"
  verify_target "$restore" "$target" || die "restore into a fresh target failed; later phases depend on it"

  say "Phase 3: source and target are independent"
  master_sql "$target" "$DB_NAME" "INSERT INTO restore_marker VALUES ('target-only')" >/dev/null
  check "a write to the target is invisible on the source" "0" \
    "$(master_sql "$SOURCE" "$DB_NAME" "SELECT count(*) FROM restore_marker WHERE label = 'target-only'")"
  check "the source keeps its post-snapshot write" "1" \
    "$(master_sql "$SOURCE" "$DB_NAME" "SELECT count(*) FROM restore_marker WHERE label = 'after-snapshot'")"

  say "Phase 4: deleting a Succeeded DBRestore never touches its target"
  local data_pvc
  data_pvc=$(jp dbinstance "$target" '{.status.resources.dataVolumeName}')
  kc delete dbrestore "$restore" --timeout=120s >/dev/null || fail "DBRestore/$restore did not delete within 120s"
  check "target DBInstance still exists" "yes" "$(exists dbinstance "$target" && echo yes || echo no)"
  check "target is not being deleted" "" "$(jp dbinstance "$target" '{.metadata.deletionTimestamp}')"
  check "target's data disk (the restore PVC) is not being deleted" "yes/" \
    "$(exists pvc "$data_pvc" && echo yes || echo no)/$(jp pvc "$data_pvc" '{.metadata.deletionTimestamp}')"
  login_ok "$target" && pass "target still serves logins" || fail "target no longer serves logins"
}

# =====================================================================
# Phase 5a (skip with SKIP_HOLD_WAIT=1) — delete the source while a backup
# of it is running: teardown must wait for the backup, leaving the VM alone,
# and new snapshots must be refused. Issues the source's delete; Phase 5
# waits for it to finish.
# =====================================================================
delete_source_during_backup() {
  local snap="rt-snap-inflight-$RUN_ID" late="rt-snap-late-$RUN_ID" uid vm
  say "Phase 5a: delete DBInstance/$SOURCE while DBSnapshot/$snap is backing it up"
  uid=$(jp dbinstance "$SOURCE" '{.metadata.uid}')
  vm=$(jp dbinstance "$SOURCE" '{.status.resources.vmName}')
  create_snapshot "$snap" "$SOURCE"
  if ! wait_until "DBSnapshot/$snap to take the snapshot hold" 120 snapshot_holds "$uid" "$snap"; then
    kc delete dbinstance "$SOURCE" --wait=false >/dev/null
    return
  fi
  kc delete dbinstance "$SOURCE" --wait=false >/dev/null
  info "Deleted DBInstance/$SOURCE while DBSnapshot/$snap holds the snapshot hold"

  if wait_until "the source's teardown to wait on DBSnapshot/$snap" 60 deletion_waits_for_snapshot "$SOURCE"; then
    pass "source deletion is waiting for the backup in progress"
    has_event DBInstance "$SOURCE" DeletionWaitingForSnapshot && pass "the wait is announced as an event" \
      || fail "no DeletionWaitingForSnapshot event on DBInstance/$SOURCE"
    if snapshot_terminal "$snap"; then
      info "DBSnapshot/$snap already finished — VM-untouched check not exercised this run"
    else
      check "the source's VM is still there, not being deleted, while the backup runs" "yes/" \
        "$(exists virtualmachines.kubevirt.io "$vm" && echo yes || echo no)/$(jp virtualmachines.kubevirt.io "$vm" '{.metadata.deletionTimestamp}')"
    fi
  elif snapshot_terminal "$snap"; then
    info "DBSnapshot/$snap finished before teardown looked — the wait was not exercised this run"
  fi

  create_snapshot "$late" "$SOURCE"
  wait_until "DBSnapshot/$late to be refused" 120 snapshot_rejected "$late" "SourceDeleting" \
    && pass "a snapshot requested after the source's deletion is refused (SourceDeleting)"
  has_event DBSnapshot "$late" SourceDeleting Warning && pass "the refusal is a Warning event" \
    || fail "no Warning SourceDeleting event on DBSnapshot/$late"

  wait_until "DBSnapshot/$snap to finish" "$BACKUP_TIMEOUT" snapshot_terminal "$snap"
  check "the backup in progress completed despite the source's deletion" "BackupReady" "$(snapshot_reason "$snap")"
}

# =====================================================================
# Phase 5 — restore after the source is gone
# =====================================================================
phase_restore_after_source_deleted() {
  local restore="rt-restore2-$RUN_ID" target="rt-tgt2-$RUN_ID"
  say "Phase 5: delete the source, then restore from the snapshot"
  record_pvcs "$SOURCE"
  local disks
  disks=$(instance_pvcs "$SOURCE" | tr '\n' ' ')
  [[ -n "$disks" ]] && info "Source disks before deletion: $disks" || fail "found no PVCs named for the source before deleting it"
  if [[ "${SKIP_HOLD_WAIT:-0}" == "1" ]]; then
    kc delete dbinstance "$SOURCE" --wait=false >/dev/null
  else
    delete_source_during_backup
  fi
  wait_until "DBInstance/$SOURCE to be deleted" 600 not_exists dbinstance "$SOURCE" \
    && pass "source DBInstance deleted"
  wait_until "the source's disks to be deleted" 300 no_instance_pvcs "$SOURCE" \
    && pass "deleting the source deleted its disks ($disks)"
  check "DBSnapshot is still Ready after its source is gone" "True" \
    "$(jp dbsnapshot "$SNAPSHOT" '{.status.conditions[?(@.type=="Ready")].status}')"
  if [[ "${SKIP_HOLD_WAIT:-0}" != "1" ]]; then
    check "the manual snapshot taken during deletion survives its source" "BackupReady" \
      "$(snapshot_reason "rt-snap-inflight-$RUN_ID")"
  fi

  create_restore "$restore" "$target" "$SNAPSHOT" "$ALLOCATED_STORAGE"
  wait_restore_terminal "$restore" "$RESTORE_TIMEOUT"
  verify_target "$restore" "$target"
}

# =====================================================================
# Phase 6 — failure paths
# =====================================================================
# expect_settled <restore> — an ended restore leaves no PVC of its own and,
# once that's gone, no hold (the hold outlives a PVC still being deleted).
expect_settled() {
  local uid; uid=$(restore_uid "$1")
  wait_until "DBRestore/$1 to leave no restore PVC behind" 300 no_restore_pvcs "$uid" \
    && pass "no restore PVC left behind by DBRestore/$1"
  wait_until "DBRestore/$1 to release its restore hold" 60 not_exists lease "$(restore_lease "$1")" \
    && pass "no restore hold left behind by DBRestore/$1"
}

expect_failed() { # expect_failed <restore> <reason>
  wait_restore_terminal "$1" "$FAIL_TIMEOUT"
  check "DBRestore/$1 failed with $2" "Failed/$2" "$(restore_stage "$1")/$(restore_reason "$1")"
  has_event DBRestore "$1" "$2" Warning && pass "the failure is a Warning event on DBRestore/$1" \
    || fail "no Warning event $2 on DBRestore/$1"
  expect_settled "$1"
}

phase_failure_paths() {
  local r t existing
  say "Phase 6a: missing snapshot"
  r="rt-nosnap-$RUN_ID"
  create_restore "$r" "rt-tgt-nosnap-$RUN_ID" "does-not-exist-$RUN_ID" "$ALLOCATED_STORAGE"
  expect_failed "$r" "SnapshotNotFound"

  if (( ALLOCATED_STORAGE > 1 )); then
    say "Phase 6b: allocatedStorage smaller than the snapshot"
    r="rt-small-$RUN_ID"
    create_restore "$r" "rt-tgt-small-$RUN_ID" "$SNAPSHOT" "$((ALLOCATED_STORAGE - 1))"
    expect_failed "$r" "AllocatedStorageTooSmall"
  fi

  # Reuse the most recent restored target as the "already taken" name.
  existing="rt-tgt2-$RUN_ID"
  exists dbinstance "$existing" || existing="rt-tgt1-$RUN_ID"
  if exists dbinstance "$existing"; then
    say "Phase 6c: target name already taken by another DBInstance"
    local before_uid
    before_uid=$(jp dbinstance "$existing" '{.spec.restoredFrom.dbRestoreUID}')
    r="rt-conflict-$RUN_ID"
    create_restore "$r" "$existing" "$SNAPSHOT" "$ALLOCATED_STORAGE"
    expect_failed "$r" "TargetNameConflict"
    check "the existing DBInstance is not being deleted" "" "$(jp dbinstance "$existing" '{.metadata.deletionTimestamp}')"
    check "the existing DBInstance still belongs to its own restore" "$before_uid" \
      "$(jp dbinstance "$existing" '{.spec.restoredFrom.dbRestoreUID}')"
  fi

  say "Phase 6d: deleting an in-progress DBRestore cancels it and deletes its target"
  r="rt-cancel-$RUN_ID"; t="rt-tgt-cancel-$RUN_ID"
  create_restore "$r" "$t" "$SNAPSHOT" "$ALLOCATED_STORAGE"
  info "Waiting up to ${RESTORE_TIMEOUT}s for the target DBInstance to be created"
  if wait_until "DBInstance/$t to be created" "$RESTORE_TIMEOUT" exists dbinstance "$t"; then
    if restore_terminal "$r"; then
      info "DBRestore/$r already finished ($(restore_stage "$r")) — cancellation not exercised this run"
    else
      record_pvcs "$t"
      local uid; uid=$(restore_uid "$r")
      kc delete dbrestore "$r" --wait=false >/dev/null
      wait_until "DBRestore/$r to be gone" 600 not_exists dbrestore "$r" \
        && pass "cancelled DBRestore/$r finished its cleanup"
      if not_exists dbinstance "$t" || [[ -n "$(jp dbinstance "$t" '{.metadata.deletionTimestamp}')" ]]; then
        pass "cancellation deleted the in-progress target DBInstance/$t"
      else
        fail "cancellation left the in-progress target DBInstance/$t running"
      fi
      wait_until "the cancelled target's disks to be deleted" 300 no_instance_pvcs "$t" \
        && pass "cancellation left no disk of DBInstance/$t behind"
      wait_until "the cancelled restore's PVC to be deleted" 300 no_restore_pvcs "$uid" \
        && pass "cancellation left no restore PVC behind"
    fi
  fi
}

# =====================================================================
# Phase 7 (skip with SKIP_DEADLINE=1) — restore deadline. Restarts the shared
# operator with a short restore.timeout, so a normal restore (~5 minutes
# here) times out while its target is starting; restores the operator
# afterwards. Runs before the snapshot-delete phase, which consumes the
# snapshot.
# =====================================================================
time_epoch() { date -u -d "$1" +%s 2>/dev/null; }

phase_deadline() {
  local r="rt-deadline-$RUN_ID" t="rt-tgt-deadline-$RUN_ID" others args created deadline
  say "Phase 7: restore deadline (operator restarted with restore.timeout=${DEADLINE_TIMEOUT_SECONDS}s)"

  args=$(ok get deploy "$OPERATOR_DEPLOY" -o jsonpath="$(operator_container_jsonpath '.args')" 2>/dev/null)
  if [[ "$args" == *"--restore."* ]]; then
    fail "operator sets restore.* by flag ($args), which would override the env vars — skipping the deadline phase"
    return
  fi
  others=$(kubectl get dbrestore -A -l "dbaas-e2e/run!=$RUN_ID" \
    -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}={.status.stage}{"\n"}{end}' 2>/dev/null |
    grep -vE '=(Succeeded|Failed)$' | grep -v '^$' || true)
  if [[ -n "$others" ]]; then
    info "Skipping: other unfinished DBRestores in the cluster would be ended by a short timeout:"
    printf '  %s\n' $others
    return
  fi

  info "Restarting the operator with DBAAS_RESTORE__TIMEOUT=${DEADLINE_TIMEOUT_SECONDS}s DBAAS_RESTORE__RECOVERY_TIMEOUT=${DEADLINE_RECOVERY_SECONDS}s"
  reconfigure_operator "$DEADLINE_TIMEOUT_SECONDS" "$DEADLINE_RECOVERY_SECONDS" \
    || { fail "could not reconfigure the operator"; return; }
  pass "operator restarted with a short restore deadline"

  create_restore "$r" "$t" "$SNAPSHOT" "$ALLOCATED_STORAGE"
  wait_restore_terminal "$r" $((DEADLINE_TIMEOUT_SECONDS + 300))
  check "DBRestore/$r timed out" "Failed/RestoreTimedOut" "$(restore_stage "$r")/$(restore_reason "$r")"
  [[ "$(jp dbrestore "$r" '{.status.message}')" == *"last stage"* ]] \
    && pass "timeout message names the stage it was stuck in" \
    || fail "timeout message doesn't name the stage: $(jp dbrestore "$r" '{.status.message}')"

  created=$(time_epoch "$(jp dbrestore "$r" '{.metadata.creationTimestamp}')")
  deadline=$(time_epoch "$(jp dbrestore "$r" '{.status.deadline}')")
  check "status.deadline is creation + restore.timeout" "$DEADLINE_TIMEOUT_SECONDS" "$(( ${deadline:-0} - ${created:-0} ))"

  if not_exists dbinstance "$t" || [[ -n "$(jp dbinstance "$t" '{.metadata.deletionTimestamp}')" ]]; then
    pass "the unfinished target DBInstance/$t was deleted"
  else
    fail "the unfinished target DBInstance/$t is still running after the timeout"
  fi
  expect_settled "$r"
  wait_until "the timed-out target's disks to be deleted" 300 no_instance_pvcs "$t" \
    && pass "the timed-out target left no disk behind"

  info "Putting the operator's restore settings back"
  restore_operator && pass "operator restore settings restored" || fail "operator restore settings NOT restored"
}

# =====================================================================
# Phase 8 — deleting the DBSnapshot while a restore reads from it (last:
# it consumes the snapshot)
# =====================================================================
backup_exists() { kc get virtualmachinebackups.harvesterhci.io "$SNAPSHOT" >/dev/null 2>&1; }

phase_snapshot_delete_race() {
  local r="rt-race-$RUN_ID" t="rt-tgt-race-$RUN_ID" lease violated=0
  say "Phase 8: delete DBSnapshot/$SNAPSHOT while DBRestore/$r is reading it"
  create_restore "$r" "$t" "$SNAPSHOT" "$ALLOCATED_STORAGE"
  wait_until "DBRestore/$r to take its restore hold" "$FAIL_TIMEOUT" restore_holds "$r" || return
  lease=$(restore_lease "$r")
  backup_exists || { info "VirtualMachineBackup not visible from this kubeconfig — skipping"; return; }

  kc delete dbsnapshot "$SNAPSHOT" --wait=false >/dev/null
  info "Watching: the backend backup must survive while the restore hold exists"
  local start; start=$(date +%s)
  until restore_terminal "$r"; do
    if exists lease "$lease" && ! backup_exists; then violated=1; break; fi
    (( $(date +%s) - start >= RESTORE_TIMEOUT )) && { fail "DBRestore/$r never finished"; break; }
    sleep 2
  done
  (( violated )) && fail "the backend backup was deleted while DBRestore/$r still held the snapshot" \
    || pass "the backend backup survived while the restore held it"

  case "$(restore_stage "$r")/$(restore_reason "$r")" in
    Succeeded/*) pass "restore finished first (its volume was already copied): Succeeded" ;;
    Failed/SnapshotDeleting|Failed/SnapshotNotFound|Failed/VolumeSnapshotMissing)
      pass "restore failed closed on the deleted snapshot: $(restore_reason "$r")"
      expect_settled "$r" ;;
    *) fail "unexpected outcome for DBRestore/$r: $(restore_stage "$r")/$(restore_reason "$r")" ;;
  esac
  wait_until "DBSnapshot/$SNAPSHOT to finish deleting once the restore released it" 900 \
    not_exists dbsnapshot "$SNAPSHOT" && pass "DBSnapshot deleted once the restore no longer held it"
}

# =====================================================================
main() {
  require kubectl; require psql; require base64; require date
  kubectl get crd dbrestores.dbaas.opencloud.wso2.com >/dev/null 2>&1 \
    || die "DBRestore CRD not installed — deploy the controller under test first"
  info "Run $RUN_ID in namespace $NAMESPACE (network $NETWORK_REF, class $DB_CLASS, ${ALLOCATED_STORAGE}Gi)"
  info "VM console login for every VM in this run: ubuntu / $VM_PASSWORD  (virtctl console pg-<instance> -n $NAMESPACE)"

  phase_seed
  phase_restore_live_source
  [[ "${SKIP_SOURCE_DELETE:-0}" == "1" ]] || phase_restore_after_source_deleted
  [[ "${SKIP_NEGATIVE:-0}" == "1" ]] || phase_failure_paths
  [[ "${SKIP_DEADLINE:-0}" == "1" ]] || phase_deadline
  [[ "${SKIP_SNAPSHOT_RACE:-0}" == "1" ]] || phase_snapshot_delete_race

  (( FAIL == 0 )) || exit 1
}

main
