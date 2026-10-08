#!/usr/bin/env bash
#
# E2E test for backup concurrency control against a real cluster.
#
# What it proves:
#   1. Global cap: with backup.maxConcurrent=K, never more than K backups
#      run at once, cluster-wide.
#   2. Fair order: within a namespace, snapshots start in creation order;
#      the rest wait as BackupQueued (status reason and an event).
#   3. Per-namespace share (needs NAMESPACE2): each namespace runs at most
#      ceil(K/2). A younger snapshot in another namespace starts before an
#      older one held back by its own namespace's share.
#   4. Backup timeout: with a short backup.timeout a backup fails as
#      BackupTimedOut and its Harvester backup is deleted.
#
# It RESTARTS THE SHARED OPERATOR with test settings (env vars
# DBAAS_BACKUP__MAX_CONCURRENT / DBAAS_BACKUP__TIMEOUT), then puts the
# originals back, on exit too. It refuses to start while any other
# DBSnapshot in the cluster is unfinished: a small cap would queue it and a
# short timeout would fail it.
#
# kubectl only (no psql), so it runs from any machine with cluster access.
#
# Usage: ./test-backup-concurrency.sh [--cleanup]
#   --cleanup  delete the instances and snapshots this run created on exit,
#              only if it passed
#
# Env (defaults in brackets):
#   NAMESPACE [default]  NETWORK_REF [vm-network-001]
#   NAMESPACE2, NETWORK_REF2  a second namespace and its network; unset
#                             skips the per-namespace check (3)
#   DB_CLASS [db.t3.medium]  ALLOCATED_STORAGE [20]  RUN_ID [epoch seconds]
#   MAX_CONCURRENT [2]       the cap under test (share = ceil(K/2) = 1)
#   TIMEOUT_SECONDS [15]     backup.timeout for the timeout check; a backup
#                            that finishes faster leaves (4) not exercised
#   PROVISION_TIMEOUT [900]  BACKUP_WAIT [1800]  POLL [2]
#   SKIP_TIMEOUT=1           skip the timeout check (4)
#   OPERATOR_NS [dbaas-system]  OPERATOR_DEPLOY [dbaas-controller-manager]
#   OPERATOR_CONTAINER [manager]
#
# Requires: kubectl, GNU date.

set -uo pipefail

NAMESPACE="${NAMESPACE:-default}"
NETWORK_REF="${NETWORK_REF:-vm-network-001}"
NAMESPACE2="${NAMESPACE2:-}"
NETWORK_REF2="${NETWORK_REF2:-}"
DB_CLASS="${DB_CLASS:-db.t3.medium}"
ALLOCATED_STORAGE="${ALLOCATED_STORAGE:-20}"
RUN_ID="${RUN_ID:-$(date +%s)}"
MAX_CONCURRENT="${MAX_CONCURRENT:-2}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-15}"
PROVISION_TIMEOUT="${PROVISION_TIMEOUT:-900}"
BACKUP_WAIT="${BACKUP_WAIT:-1800}"
POLL="${POLL:-2}"
OPERATOR_NS="${OPERATOR_NS:-dbaas-system}"
OPERATOR_DEPLOY="${OPERATOR_DEPLOY:-dbaas-controller-manager}"
OPERATOR_CONTAINER="${OPERATOR_CONTAINER:-manager}"
PER_NAMESPACE=$(( (MAX_CONCURRENT + 1) / 2 )); (( PER_NAMESPACE < 1 )) && PER_NAMESPACE=1

CLEANUP=false
for arg in "$@"; do
  case "$arg" in
    --cleanup) CLEANUP=true ;;
    -h|--help) sed -n '2,45p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

RUN_LABEL="dbaas-e2e/run=$RUN_ID"
# Instances: three in NAMESPACE (same-namespace order and share), one in
# NAMESPACE2 (cross-namespace share). One snapshot per instance per burst,
# so the per-instance snapshot hold never confounds the queue.
A_INSTANCES=("cc-a1-$RUN_ID" "cc-a2-$RUN_ID" "cc-a3-$RUN_ID")
B_INSTANCE="cc-b1-$RUN_ID"
CREATED=()   # "<ns>/<kind>/<name>" this run created, for --cleanup
PASS=0
FAIL=0

# ---------- output ----------
say()  { printf '\n\033[1;36m[%(%H:%M:%S)T] == %s ==\033[0m\n' -1 "$*"; }
info() { printf '[%(%H:%M:%S)T] %s\n' -1 "$*"; }
pass() { printf '\033[1;32mPASS\033[0m  %s\n' "$*"; PASS=$((PASS + 1)); }
fail() { printf '\033[1;31mFAIL\033[0m  %s\n' "$*"; FAIL=$((FAIL + 1)); }
die()  { printf '\033[1;31mABORT\033[0m %s\n' "$*"; exit 1; }
check() { if [[ "$3" == "$2" ]]; then pass "$1"; else fail "$1 (want '$2', got '$3')"; fi; }

kn() { local ns="$1"; shift; kubectl -n "$ns" "$@"; }

wait_until() { # wait_until <description> <timeout-seconds> <command...>
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

# ---------- operator configuration ----------
OPERATOR_RECONFIGURED=false
declare -A OPERATOR_ENV_BEFORE=() # var -> original value; absent key = was unset
BACKUP_ENV_VARS=(DBAAS_BACKUP__MAX_CONCURRENT DBAAS_BACKUP__TIMEOUT)

ok() { kubectl -n "$OPERATOR_NS" "$@"; }
operator_container_jsonpath() { echo "{.spec.template.spec.containers[?(@.name=='$OPERATOR_CONTAINER')]$1}"; }

operator_env_value() { # prints the value and succeeds if the container sets <var>
  local names
  names=$(ok get deploy "$OPERATOR_DEPLOY" -o jsonpath="$(operator_container_jsonpath '.env[*].name')" 2>/dev/null)
  grep -qxF "$1" <<<"${names// /$'\n'}" || return 1
  ok get deploy "$OPERATOR_DEPLOY" -o jsonpath="$(operator_container_jsonpath ".env[?(@.name=='$1')].value")"
}

operator_rollout() { ok rollout status deploy/"$OPERATOR_DEPLOY" --timeout=300s >/dev/null; }

reconfigure_operator() { # reconfigure_operator VAR=value...
  local v val
  if [[ "$OPERATOR_RECONFIGURED" != "true" ]]; then
    for v in "${BACKUP_ENV_VARS[@]}"; do
      if val=$(operator_env_value "$v"); then OPERATOR_ENV_BEFORE[$v]="$val"; fi
    done
  fi
  OPERATOR_RECONFIGURED=true
  ok set env deploy/"$OPERATOR_DEPLOY" -c "$OPERATOR_CONTAINER" "$@" >/dev/null && operator_rollout
}

restore_operator() { # puts back exactly what was there before; idempotent
  [[ "$OPERATOR_RECONFIGURED" == "true" ]] || return 0
  local v args=()
  for v in "${BACKUP_ENV_VARS[@]}"; do
    if [[ -v "OPERATOR_ENV_BEFORE[$v]" ]]; then args+=("$v=${OPERATOR_ENV_BEFORE[$v]}"); else args+=("$v-"); fi
  done
  if ok set env deploy/"$OPERATOR_DEPLOY" -c "$OPERATOR_CONTAINER" "${args[@]}" >/dev/null && operator_rollout; then
    OPERATOR_RECONFIGURED=false
    info "Operator backup settings put back (${args[*]})"
    return 0
  fi
  printf '\033[1;31mOPERATOR NOT RESTORED\033[0m put it back by hand:\n  kubectl -n %s set env deploy/%s -c %s %s\n' \
    "$OPERATOR_NS" "$OPERATOR_DEPLOY" "$OPERATOR_CONTAINER" "${args[*]}"
  return 1
}

# ---------- resources ----------
provision() { # provision <ns> <name> <network>
  CREATED+=("$1/dbinstance/$2")
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: $2
  namespace: $1
  labels:
    dbaas-e2e/run: "$RUN_ID"
spec:
  dbInstanceClass: $DB_CLASS
  allocatedStorage: $ALLOCATED_STORAGE
  networkRef: $3
  backup: {}
EOF
}

available() { [[ "$(kn "$1" get dbinstance "$2" -o jsonpath='{.status.phase}' 2>/dev/null)" == "available" ]]; }

create_snapshot() { # create_snapshot <ns> <snapshot> <source>
  CREATED+=("$1/dbsnapshot/$2")
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBSnapshot
metadata:
  name: $2
  namespace: $1
  labels:
    dbaas-e2e/run: "$RUN_ID"
spec:
  sourceInstanceRef:
    name: $3
EOF
}

snap_reason() { kn "$1" get dbsnapshot "$2" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}' 2>/dev/null; }
snap_terminal() {
  case "$(snap_reason "$1" "$2")" in
    BackupReady|BackupFailed|BackupTimedOut|Source*) return 0 ;;
    *) return 1 ;;
  esac
}

# other_unfinished lists DBSnapshots outside this run that haven't finished.
other_unfinished() {
  kubectl get dbsnapshot -A -l "dbaas-e2e/run!=$RUN_ID" \
    -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}={.status.conditions[?(@.type=="Ready")].reason}{"\n"}{end}' 2>/dev/null |
    grep -vE '=(BackupReady|BackupFailed|BackupTimedOut|Source[A-Za-z]*)$' | grep -v '^$' || true
}

on_exit() {
  local code=$?
  restore_operator || code=1
  printf '\n\033[1m%d passed, %d failed\033[0m (run %s)\n' "$PASS" "$FAIL" "$RUN_ID"
  if [[ "$CLEANUP" != "true" || "$code" != "0" || "$FAIL" != "0" ]]; then
    info "Leaving test resources in place. To remove them later:"
    info "  kubectl delete dbsnapshot -A -l $RUN_LABEL"
    info "  kubectl delete dbinstance -A -l $RUN_LABEL"
    exit "$code"
  fi
  say "Cleaning up"
  kubectl delete dbsnapshot -A -l "$RUN_LABEL" --ignore-not-found --timeout=600s
  kubectl delete dbinstance -A -l "$RUN_LABEL" --ignore-not-found --timeout=900s
  exit "$code"
}
trap on_exit EXIT

# =====================================================================
# Setup: instances, and an operator nobody else is using for backups
# =====================================================================
phase_setup() {
  say "Setup: provision ${#A_INSTANCES[@]} instance(s) in $NAMESPACE${NAMESPACE2:+ and 1 in $NAMESPACE2}"
  local others i args
  # Flags override env vars: settings this test sets by env would be ignored.
  args=$(ok get deploy "$OPERATOR_DEPLOY" -o jsonpath="$(operator_container_jsonpath '.args')" 2>/dev/null)
  [[ "$args" != *"--backup."* ]] || die "the operator sets backup.* by flag ($args), which would override this test's env vars"
  others=$(other_unfinished)
  [[ -z "$others" ]] || die "other unfinished DBSnapshots in the cluster would be queued/failed by the test settings:
$others"

  for i in "${A_INSTANCES[@]}"; do provision "$NAMESPACE" "$i" "$NETWORK_REF"; done
  [[ -n "$NAMESPACE2" ]] && provision "$NAMESPACE2" "$B_INSTANCE" "$NETWORK_REF2"
  for i in "${A_INSTANCES[@]}"; do
    wait_until "DBInstance $NAMESPACE/$i available" "$PROVISION_TIMEOUT" available "$NAMESPACE" "$i" || die "provisioning failed"
  done
  if [[ -n "$NAMESPACE2" ]]; then
    wait_until "DBInstance $NAMESPACE2/$B_INSTANCE available" "$PROVISION_TIMEOUT" available "$NAMESPACE2" "$B_INSTANCE" || die "provisioning failed"
  fi
  pass "instances available"
}

# =====================================================================
# Phase 1: a burst of snapshots under a small cap
# =====================================================================
phase_burst() {
  say "Phase 1: burst of snapshots with backup.maxConcurrent=$MAX_CONCURRENT (per-namespace share $PER_NAMESPACE)"
  reconfigure_operator "DBAAS_BACKUP__MAX_CONCURRENT=$MAX_CONCURRENT" || die "could not reconfigure the operator"
  pass "operator restarted with backup.maxConcurrent=$MAX_CONCURRENT"

  # Created oldest-first, a second apart so creation order is unambiguous;
  # B's snapshot is the youngest.
  local snaps=() i
  for i in "${A_INSTANCES[@]}"; do
    create_snapshot "$NAMESPACE" "snap-$i" "$i"; snaps+=("$NAMESPACE/snap-$i"); sleep 1
  done
  if [[ -n "$NAMESPACE2" ]]; then
    create_snapshot "$NAMESPACE2" "snap-$B_INSTANCE" "$B_INSTANCE"; snaps+=("$NAMESPACE2/snap-$B_INSTANCE")
  fi

  # Sample until every snapshot has finished: how many run at once (total
  # and per namespace), and the order they start in.
  local start now line key reason total done max_total=0 started_order=()
  declare -A started=() max_ns=() running_ns=() queued_seen=()
  start=$(date +%s)
  while :; do
    total=0; done=0; running_ns=()
    while IFS='=' read -r key reason; do
      [[ -n "$key" ]] || continue
      case "$reason" in
        BackupInProgress)
          total=$((total + 1)); running_ns[${key%%/*}]=$(( ${running_ns[${key%%/*}]:-0} + 1 ))
          [[ -v "started[$key]" ]] || { started[$key]=1; started_order+=("$key"); } ;;
        BackupReady|BackupFailed|BackupTimedOut)
          done=$((done + 1))
          [[ -v "started[$key]" ]] || { started[$key]=1; started_order+=("$key"); } ;;
        Source*) done=$((done + 1)) ;; # rejected: never started
        BackupQueued) queued_seen[$key]=1 ;;
      esac
    done < <(kubectl get dbsnapshot -A -l "$RUN_LABEL" \
      -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}={.status.conditions[?(@.type=="Ready")].reason}{"\n"}{end}' 2>/dev/null)
    (( total > max_total )) && max_total=$total
    for key in "${!running_ns[@]}"; do
      (( ${running_ns[$key]} > ${max_ns[$key]:-0} )) && max_ns[$key]=${running_ns[$key]}
    done
    (( done >= ${#snaps[@]} )) && break
    now=$(date +%s)
    (( now - start >= BACKUP_WAIT )) && { fail "snapshots not all finished after ${BACKUP_WAIT}s"; break; }
    sleep "$POLL"
  done
  info "Start order: ${started_order[*]}"

  (( max_total <= MAX_CONCURRENT )) && pass "never more than $MAX_CONCURRENT backups at once (max seen: $max_total)" \
    || fail "$max_total backups ran at once, cap $MAX_CONCURRENT"
  for key in "${!max_ns[@]}"; do
    (( ${max_ns[$key]} <= PER_NAMESPACE )) && pass "namespace $key never ran more than $PER_NAMESPACE at once (max seen: ${max_ns[$key]})" \
      || fail "namespace $key ran ${max_ns[$key]} at once, share $PER_NAMESPACE"
  done
  for line in "${snaps[@]}"; do
    check "$line finished as BackupReady" "BackupReady" "$(snap_reason "${line%%/*}" "${line#*/}")"
  done

  # Same-namespace order is creation order.
  local a_order=()
  for key in "${started_order[@]}"; do [[ "$key" == "$NAMESPACE/"* ]] && a_order+=("$key"); done
  check "snapshots in $NAMESPACE started in creation order" \
    "$NAMESPACE/snap-${A_INSTANCES[0]} $NAMESPACE/snap-${A_INSTANCES[1]} $NAMESPACE/snap-${A_INSTANCES[2]}" "${a_order[*]}"

  # Something had to wait, visibly.
  local queued="$NAMESPACE/snap-${A_INSTANCES[2]}"
  if [[ -v "queued_seen[$queued]" ]]; then
    pass "$queued waited as BackupQueued"
  else
    fail "$queued was never seen as BackupQueued"
  fi
  [[ -n "$(kn "$NAMESPACE" get events --field-selector "involvedObject.kind=DBSnapshot,involvedObject.name=snap-${A_INSTANCES[2]},reason=BackupQueued" -o name 2>/dev/null)" ]] \
    && pass "the wait is announced as a BackupQueued event" || fail "no BackupQueued event on $queued"

  # Cross-namespace share: B's snapshot is the youngest, but A is held to
  # its share, so B starts before A's second snapshot.
  if [[ -n "$NAMESPACE2" ]]; then
    local pos_b=-1 pos_a2=-1 idx
    for idx in "${!started_order[@]}"; do
      [[ "${started_order[$idx]}" == "$NAMESPACE2/snap-$B_INSTANCE" ]] && pos_b=$idx
      [[ "${started_order[$idx]}" == "$NAMESPACE/snap-${A_INSTANCES[1]}" ]] && pos_a2=$idx
    done
    (( pos_b >= 0 && pos_a2 >= 0 && pos_b < pos_a2 )) \
      && pass "$NAMESPACE2's younger snapshot started before $NAMESPACE's older queued one (namespace share)" \
      || fail "$NAMESPACE2's snapshot did not start ahead of $NAMESPACE's queued backlog (order: ${started_order[*]})"
  else
    info "NAMESPACE2 not set — per-namespace share across namespaces not exercised"
  fi
}

# =====================================================================
# Phase 2 (skip with SKIP_TIMEOUT=1): a backup that outlives backup.timeout
# =====================================================================
phase_timeout() {
  local snap="snap-timeout-$RUN_ID" ns="$NAMESPACE" reason
  say "Phase 2: backup.timeout=${TIMEOUT_SECONDS}s"
  reconfigure_operator "DBAAS_BACKUP__TIMEOUT=${TIMEOUT_SECONDS}s" || { fail "could not reconfigure the operator"; return; }
  pass "operator restarted with backup.timeout=${TIMEOUT_SECONDS}s"

  create_snapshot "$ns" "$snap" "${A_INSTANCES[0]}"
  wait_until "DBSnapshot $ns/$snap to finish" 600 snap_terminal "$ns" "$snap" || return
  reason=$(snap_reason "$ns" "$snap")
  case "$reason" in
    BackupTimedOut)
      pass "the backup failed as BackupTimedOut"
      if kubectl get crd virtualmachinebackups.harvesterhci.io >/dev/null 2>&1; then
        wait_until "its Harvester backup to be deleted" 300 bash -c "! kubectl -n '$ns' get virtualmachinebackups.harvesterhci.io '$snap' >/dev/null 2>&1" \
          && pass "the timed-out Harvester backup was deleted"
      fi
      [[ -n "$(kn "$ns" get events --field-selector "involvedObject.kind=DBSnapshot,involvedObject.name=$snap,reason=BackupTimedOut,type=Warning" -o name 2>/dev/null)" ]] \
        && pass "the timeout is a Warning event" || fail "no Warning BackupTimedOut event on $ns/$snap"
      ;;
    BackupReady) info "the backup finished within ${TIMEOUT_SECONDS}s — timeout not exercised this run (lower TIMEOUT_SECONDS)" ;;
    *) fail "unexpected outcome for $ns/$snap: $reason" ;;
  esac
  restore_operator && pass "operator backup settings restored" || fail "operator backup settings NOT restored"
}

main() {
  command -v kubectl >/dev/null || die "kubectl not found"
  kubectl get crd dbsnapshots.dbaas.opencloud.wso2.com >/dev/null 2>&1 || die "DBSnapshot CRD not installed"
  info "Run $RUN_ID: $NAMESPACE${NAMESPACE2:+ + $NAMESPACE2}, cap $MAX_CONCURRENT (share $PER_NAMESPACE)"
  phase_setup
  phase_burst
  [[ "${SKIP_TIMEOUT:-0}" == "1" ]] || phase_timeout
  (( FAIL == 0 )) || exit 1
}

main
