#!/usr/bin/env bash
#
# E2E test for automated snapshot scheduling: provisions a
# DBInstance with backup enabled, forces its schedule due immediately
# (waiting for the real daily window isn't practical here), and verifies the
# automated DBSnapshot gets created and completes.
#
# Deletion check (skip with SKIP_DELETE_CHECK=1 to keep the instance):
# the automated snapshot is owned by its instance's UID, so deleting the
# instance garbage-collects it — and its Harvester backup — along with the
# instance's own disks.
#
# Requires kubectl >= 1.24 (jsonpath/create wait conditions, status
# subresource patch) and GNU date.
#
# Usage: ./test-automated-backup.sh [--cleanup]
# Config via env vars: NAMESPACE, INSTANCE_NAME, NETWORK_REF, DB_CLASS,
# ALLOCATED_STORAGE, PROVISION_TIMEOUT, SCHEDULE_TIMEOUT, BACKUP_TIMEOUT,
# DELETE_TIMEOUT, POLL_INTERVAL, SKIP_DELETE_CHECK

set -euo pipefail

NAMESPACE="${NAMESPACE:-default}"
# Suffixed with epoch seconds by default so repeat/concurrent runs don't
# collide; set INSTANCE_NAME explicitly to reuse a fixed name.
INSTANCE_NAME="${INSTANCE_NAME:-db-test-auto-schedule-$(date +%s)}"
NETWORK_REF="${NETWORK_REF:-vm-network-001}"
DB_CLASS="${DB_CLASS:-db.t3.medium}"
ALLOCATED_STORAGE="${ALLOCATED_STORAGE:-20}"
PROVISION_TIMEOUT="${PROVISION_TIMEOUT:-600}" # seconds to wait for status.phase=available
SCHEDULE_TIMEOUT="${SCHEDULE_TIMEOUT:-120}"   # seconds to wait for status.backup / the DBSnapshot to appear
BACKUP_TIMEOUT="${BACKUP_TIMEOUT:-600}"       # seconds to wait for the backend backup to finish
DELETE_TIMEOUT="${DELETE_TIMEOUT:-600}"       # seconds to wait for the instance, its snapshot and disks to go
POLL_INTERVAL="${POLL_INTERVAL:-5}"           # only used waiting for status.backup to first appear

CLEANUP=false
for arg in "$@"; do
  case "$arg" in
    --cleanup) CLEANUP=true ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 1 ;;
  esac
done

DUE_TIME=""
EXPECTED_SNAPSHOT_NAME=""
INSTANCE_UID=""
INSTANCE_DISKS=""
BG_PIDS=()

log() { printf '\n[%(%H:%M:%S)T] %s\n' -1 "$1"; }
fail() { log "FAILED: $1"; exit 1; }
kc() { kubectl -n "$NAMESPACE" "$@"; }

require() {
  command -v "$1" >/dev/null 2>&1 || fail "required tool '$1' not found on PATH"
}

# Runs on any exit (success, fail(), or a `set -e` error): stops leftover
# background kubectl wait jobs, and deletes the test resources if --cleanup
# was passed, on either outcome.
on_exit() {
  local pid
  for pid in "${BG_PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done

  [[ "$CLEANUP" == "true" ]] || { log "Leaving test resources in place (pass --cleanup to remove them)"; return; }
  log "Cleaning up test resources"
  # The instance first: its automated snapshot goes with it (garbage
  # collection); deleting the snapshot by name is only a backstop.
  kc delete dbinstance "$INSTANCE_NAME" --ignore-not-found --timeout="${DELETE_TIMEOUT}s" || true
  [[ -n "$EXPECTED_SNAPSHOT_NAME" ]] && kc delete dbsnapshot "$EXPECTED_SNAPSHOT_NAME" --ignore-not-found || true
}
trap on_exit EXIT

# wait_for <description> <timeout_seconds> <predicate_fn> — only used where
# kubectl wait has no matching condition (field becomes non-empty).
wait_for() {
  local desc="$1" timeout="$2" predicate="$3"
  local start elapsed
  start=$(date +%s)
  until "$predicate"; do
    elapsed=$(( $(date +%s) - start ))
    if (( elapsed >= timeout )); then
      fail "timed out after ${timeout}s waiting for: $desc"
    fi
    sleep "$POLL_INTERVAL"
  done
}

provision() {
  log "Provisioning DBInstance '$INSTANCE_NAME' (namespace '$NAMESPACE') with backup enabled"
  cat <<EOF | kubectl apply -f -
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: ${INSTANCE_NAME}
  namespace: ${NAMESPACE}
spec:
  dbInstanceClass: ${DB_CLASS}
  allocatedStorage: ${ALLOCATED_STORAGE}
  networkRef: ${NETWORK_REF}
  dbName: appdb
  masterUsername: dbadmin
  running: true
  backup: {} # defaults: automated.enabled=true, retainCount=7, preferredWindowUTC=02:00-03:00
EOF
}

wait_available() {
  log "Waiting up to ${PROVISION_TIMEOUT}s (kubectl wait) for status.phase=available"
  kc wait dbinstance/"$INSTANCE_NAME" --for='jsonpath={.status.phase}=available' \
    --timeout="${PROVISION_TIMEOUT}s" >/dev/null \
    || fail "DBInstance never reached status.phase=available within ${PROVISION_TIMEOUT}s"
  log "Instance is available"
}

schedule_is_set() {
  [[ -n "$(kc get dbinstance "$INSTANCE_NAME" -o jsonpath='{.status.backup.nextScheduledSnapshotTime}' 2>/dev/null)" ]]
}

wait_schedule_computed() {
  log "Waiting up to ${SCHEDULE_TIMEOUT}s for the scheduler to compute status.backup.nextScheduledSnapshotTime"
  wait_for "next schedule computed" "$SCHEDULE_TIMEOUT" schedule_is_set
}

# Rewinds the persisted schedule by whole days rather than inventing a
# timestamp: the hash-derived time-of-day doesn't depend on the date, so
# subtracting N*24h keeps it valid for the scheduler's window-change check
# while landing safely in the past.
force_due() {
  local current rewind_days=2 candidate_name
  current=$(kc get dbinstance "$INSTANCE_NAME" -o jsonpath='{.status.backup.nextScheduledSnapshotTime}')
  log "Current NextScheduledSnapshotTime: $current"

  # Skip past any date already used by a same-named DBSnapshot from an earlier run.
  while (( rewind_days <= 30 )); do
    DUE_TIME=$(date -u -d "${current} -${rewind_days} days" +%Y-%m-%dT%H:%M:%SZ)
    candidate_name="${INSTANCE_NAME}-auto-$(date -u -d "$DUE_TIME" +%Y%m%d)"
    if ! kc get dbsnapshot "$candidate_name" >/dev/null 2>&1; then
      EXPECTED_SNAPSHOT_NAME="$candidate_name"
      break
    fi
    rewind_days=$(( rewind_days + 1 ))
  done
  [[ -n "$EXPECTED_SNAPSHOT_NAME" ]] || fail "could not find a free rewind date after 30 attempts"

  log "Forcing it due (rewound $rewind_days day(s)) -> $DUE_TIME"
  log "Expected automated DBSnapshot name: $EXPECTED_SNAPSHOT_NAME"
  kc patch dbinstance "$INSTANCE_NAME" --subresource=status --type=merge \
    -p "{\"status\":{\"backup\":{\"nextScheduledSnapshotTime\":\"${DUE_TIME}\"}}}" >/dev/null
}

trigger_reconcile() {
  log "Triggering a fresh reconcile"
  kc annotate dbinstance "$INSTANCE_NAME" "test.dbaas.opencloud.wso2.com/trigger=$(date +%s)" --overwrite >/dev/null
}

wait_snapshot_created() {
  log "Waiting up to ${SCHEDULE_TIMEOUT}s (kubectl wait) for DBSnapshot/$EXPECTED_SNAPSHOT_NAME to be created"
  kc wait dbsnapshot/"$EXPECTED_SNAPSHOT_NAME" --for=create --timeout="${SCHEDULE_TIMEOUT}s" >/dev/null \
    || fail "DBSnapshot/$EXPECTED_SNAPSHOT_NAME was not created within ${SCHEDULE_TIMEOUT}s"
  log "DBSnapshot/$EXPECTED_SNAPSHOT_NAME created"
}

# Owned by the instance's UID (controller reference), so it goes with the
# instance — and never with a same-named instance created later.
verify_owner_reference() {
  local owner
  INSTANCE_UID=$(kc get dbinstance "$INSTANCE_NAME" -o jsonpath='{.metadata.uid}')
  owner=$(kc get dbsnapshot "$EXPECTED_SNAPSHOT_NAME" \
    -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}/{.uid}/controller={.controller}{"\n"}{end}')
  grep -qxF "DBInstance/$INSTANCE_NAME/$INSTANCE_UID/controller=true" <<<"$owner" \
    || fail "DBSnapshot/$EXPECTED_SNAPSHOT_NAME owners = '$owner', want a controller reference to DBInstance/$INSTANCE_NAME/$INSTANCE_UID"
  log "DBSnapshot/$EXPECTED_SNAPSHOT_NAME is owned by DBInstance/$INSTANCE_NAME (UID $INSTANCE_UID)"
}

created_event_recorded() {
  [[ -n "$(kc get events -o name --field-selector \
    "involvedObject.kind=DBInstance,involvedObject.name=$INSTANCE_NAME,reason=ScheduledSnapshotCreated" 2>/dev/null)" ]]
}

verify_created_event() { # events are sent asynchronously: allow a moment
  wait_for "a ScheduledSnapshotCreated event on DBInstance/$INSTANCE_NAME" 30 created_event_recorded
  log "ScheduledSnapshotCreated event recorded on DBInstance/$INSTANCE_NAME"
}

ready_reason() {
  kc get dbsnapshot "$EXPECTED_SNAPSHOT_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}' 2>/dev/null
}

backup_is_terminal() {
  local reason
  reason=$(ready_reason)
  [[ "$reason" == "BackupReady" || "$reason" == "BackupFailed" ]]
}

# "Reason is BackupReady OR BackupFailed" can't be one kubectl wait call (its
# jsonpath condition only matches a single value), so this races two watches
# in the background and stops once either wins.
wait_backup_complete() {
  log "Waiting up to ${BACKUP_TIMEOUT}s (kubectl wait, racing both terminal reasons) for the backup to finish"

  kc wait dbsnapshot/"$EXPECTED_SNAPSHOT_NAME" \
    --for='jsonpath={.status.conditions[?(@.type=="Ready")].reason}=BackupReady' \
    --timeout="${BACKUP_TIMEOUT}s" >/dev/null 2>&1 &
  local ready_pid=$!
  kc wait dbsnapshot/"$EXPECTED_SNAPSHOT_NAME" \
    --for='jsonpath={.status.conditions[?(@.type=="Ready")].reason}=BackupFailed' \
    --timeout="${BACKUP_TIMEOUT}s" >/dev/null 2>&1 &
  local failed_pid=$!
  BG_PIDS=("$ready_pid" "$failed_pid")

  while kill -0 "$ready_pid" 2>/dev/null && kill -0 "$failed_pid" 2>/dev/null; do
    sleep 1
  done
  kill "$ready_pid" "$failed_pid" 2>/dev/null || true
  wait "$ready_pid" "$failed_pid" 2>/dev/null || true
  BG_PIDS=()

  backup_is_terminal || fail "timed out after ${BACKUP_TIMEOUT}s waiting for the backup to reach a terminal state"
}

verify_schedule_advanced() {
  local newnext
  newnext=$(kc get dbinstance "$INSTANCE_NAME" -o jsonpath='{.status.backup.nextScheduledSnapshotTime}')
  if [[ "$newnext" == "$DUE_TIME" ]]; then
    fail "schedule did not advance past the forced slot (still $DUE_TIME)"
  fi
  log "Schedule advanced past the forced slot -> $newnext"
}

check_harvester_backend() {
  if kubectl get crd virtualmachinebackups.harvesterhci.io >/dev/null 2>&1; then
    log "Harvester VirtualMachineBackup for $EXPECTED_SNAPSHOT_NAME:"
    kc get virtualmachinebackups.harvesterhci.io "$EXPECTED_SNAPSHOT_NAME" -o wide 2>/dev/null \
      || log "(not found under that name — check the Harvester UI's Backup tab manually)"
  else
    log "harvesterhci.io CRDs not visible from this kubeconfig context — skipping backend check"
  fi
}

report() {
  local reason origin label
  reason=$(ready_reason)
  origin=$(kc get dbsnapshot "$EXPECTED_SNAPSHOT_NAME" -o jsonpath='{.status.origin}')
  label=$(kc get dbsnapshot "$EXPECTED_SNAPSHOT_NAME" -o jsonpath='{.metadata.labels.dbaas\.opencloud\.wso2\.com/snapshot-origin}')

  log "Result: Ready.reason=$reason  status.origin=$origin  origin label=$label"

  if [[ "$reason" != "BackupReady" ]]; then
    kc get dbsnapshot "$EXPECTED_SNAPSHOT_NAME" -o yaml
    fail "automated backup did not complete successfully (Ready.reason=$reason)"
  fi
  if [[ "$origin" != "Automated" || "$label" != "Automated" ]]; then
    fail "origin bookkeeping is wrong: status.origin=$origin, label=$label (want Automated/Automated)"
  fi

  log "PASS: automated scheduling created DBSnapshot/$EXPECTED_SNAPSHOT_NAME and its backup completed"
}

# ---- Deleting the instance removes its automated snapshot and disks ----

instance_disks() { # PVCs named for the instance: pg-<name>-<uid salt>-...
  kc get pvc -o name 2>/dev/null | sed 's|^persistentvolumeclaim/||' | grep -E "^pg-${INSTANCE_NAME}-" || true
}
instance_gone() { ! kc get dbinstance "$INSTANCE_NAME" >/dev/null 2>&1; }
snapshot_gone() { ! kc get dbsnapshot "$EXPECTED_SNAPSHOT_NAME" >/dev/null 2>&1; }
vmbackup_gone() { ! kc get virtualmachinebackups.harvesterhci.io "$EXPECTED_SNAPSHOT_NAME" >/dev/null 2>&1; }
disks_gone() { [[ -z "$(instance_disks)" ]]; }

verify_deleted_with_instance() {
  local backend=false
  INSTANCE_DISKS=$(instance_disks | tr '\n' ' ')
  [[ -n "$INSTANCE_DISKS" ]] || fail "found no PVCs named for DBInstance/$INSTANCE_NAME before deleting it"
  kubectl get crd virtualmachinebackups.harvesterhci.io >/dev/null 2>&1 && ! vmbackup_gone && backend=true

  log "Deleting DBInstance/$INSTANCE_NAME (disks: $INSTANCE_DISKS)"
  kc delete dbinstance "$INSTANCE_NAME" --wait=false >/dev/null
  wait_for "DBInstance/$INSTANCE_NAME to be deleted" "$DELETE_TIMEOUT" instance_gone
  log "DBInstance/$INSTANCE_NAME deleted"

  wait_for "automated DBSnapshot/$EXPECTED_SNAPSHOT_NAME to be garbage-collected with its instance" "$DELETE_TIMEOUT" snapshot_gone
  log "Automated DBSnapshot/$EXPECTED_SNAPSHOT_NAME went with its instance"
  if [[ "$backend" == "true" ]]; then
    wait_for "Harvester VirtualMachineBackup $EXPECTED_SNAPSHOT_NAME to be deleted" "$DELETE_TIMEOUT" vmbackup_gone
    log "Its Harvester VirtualMachineBackup was deleted too"
  else
    log "VirtualMachineBackup not visible from this kubeconfig — skipping the backend check"
  fi

  wait_for "the instance's disks to be deleted" "$DELETE_TIMEOUT" disks_gone
  log "The instance's disks were deleted ($INSTANCE_DISKS)"
  log "PASS: deleting the instance removed its automated snapshot, its backup and its disks"
}

main() {
  require kubectl
  require date

  provision
  wait_available
  wait_schedule_computed
  force_due
  trigger_reconcile
  wait_snapshot_created
  verify_owner_reference
  verify_created_event
  wait_backup_complete
  verify_schedule_advanced
  check_harvester_backend
  report
  [[ "${SKIP_DELETE_CHECK:-0}" == "1" ]] || verify_deleted_with_instance
}

main
