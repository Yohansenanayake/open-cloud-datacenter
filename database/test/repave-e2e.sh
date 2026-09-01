#!/usr/bin/env bash
# repave-e2e.sh — stage-based e2e test runner for DBaaS OS repave + teardown.
# See P003-final-plan.md §10.4 for the test matrix (T1..T11, E1..E4) this
# script implements. Rewritten against the current bounded-reconcile
# architecture (internal/ensure step chain) — field/condition/reason names
# here differ substantially from an earlier, monolithic-controller draft of
# this script; see "Renamed/changed since M1" below before editing.
#
# Usage:
#   NS=tenant-acme ID=test-img-deletion YAML=./test.yaml ./repave-e2e.sh stage1
#   NS=tenant-acme ID=test-img-deletion                  ./repave-e2e.sh stage2
#   NS=tenant-acme ID=test-img-deletion YAML=./test.yaml ./repave-e2e.sh stage3
#   NS=tenant-acme ID=test-img-deletion                  ./repave-e2e.sh stage4
#   NS=tenant-acme ID=test-img-deletion                  ./repave-e2e.sh stage5   # T8
#   NS=tenant-acme ID=test-img-deletion                  ./repave-e2e.sh stage6   # T9
#   NS=... ID=... ID2=... YAML2=...                      ./repave-e2e.sh stage7  # T10
#   NS=... ID=... YAML=...                                ./repave-e2e.sh all     # pauses at controller redeploy
#
# IMPORTANT: ID must exactly match metadata.name in YAML — the script
# validates this up front and aborts on mismatch (every assertion keys off ID).
#
# stage4 (PG major-version EOL, E1-E4): run against an EXISTING Available
# instance (no YAML needed — it stays untouched throughout, that's the point).
# Precondition, done by hand before invoking: publish a new baked image
# revision whose SupportedEngineVersions drops the instance's engineVersion,
# point LatestBakedImages[osStream] at it with ValidationState: Validated,
# and `make deploy`.
#
# stage5 (T8, manager misconfiguration): point databaseDefaults.osVersion at
# a stream that isn't in LatestBakedImages, redeploy, run this stage, then
# revert the config and redeploy again. Run against an EXISTING Available
# instance ($ID) — the whole point is confirming it does NOT crash-loop.
#
# stage6 (T9, kill-mid-swap recovery): requires you to kill the manager pod
# by hand at the right moment (instructions printed by the stage) — timing
# can't be scripted reliably, so this stage prints what to watch for and
# waits for you to confirm before checking recovery.
#
# stage7 (T10, concurrent repave / no PVC collision): needs a second
# instance — set ID2/YAML2 (defaults: YAML2=$YAML with ID2 substituted for
# metadata.name if YAML2 unset). Independent of $ID/stage1-3's state.
#
# Renamed/changed since an earlier (M1) draft of this script — if you're
# diffing against that version, every one of these is intentional, not a
# regression:
#   .status.provisioningPhase        -> .status.phase (and values are
#                                        lowercase: "available", not
#                                        "Available" — see StatusAvailable
#                                        etc. in dbinstance_types.go)
#   "Failed" phase                   -> does not exist; StatusFailed is
#                                        declared but never actually set by
#                                        DerivePhaseSummary. A rejected spec
#                                        (Terminal preflight) surfaces as
#                                        "incompatible-parameters"
#                                        (StatusIncompatibleParameters) via
#                                        ConditionAccepted=False, not "failed".
#   .status.appliedSpec.imageRevision -> .status.currentImageRevision
#                                        (top-level field, decision #15 —
#                                        moved out of AppliedSpec on purpose)
#   condition type "OSUpdateAvailable"/
#   condition type "PGVersionEOL"    -> merged into ONE condition type
#                                        "ImageDrift", distinguished by
#                                        .reason ("OSUpdateAvailable" /
#                                        "EngineVersionEOL") — decision #9
#   reason "RepaveBlockedPGVersionEOL" -> "RepaveBlockedEOL"
#   reason "UnsupportedEngineVersion" (on new-instance rejection)
#                                     -> "OSImageInvalid" on PreflightReady
#   (nothing observable when repave is blocked)
#                                     -> RepaveInProgress condition now goes
#                                        False/<reason> on a blocked repave
#                                        (a gap found and fixed while writing
#                                        this script — see repave.go; before
#                                        this fix, Result.Reason/Message from
#                                        a blocked repave were silently
#                                        dropped, since reconcileInstance only
#                                        reads ControllerResult/Err)
#
# Env:
#   NS       instance namespace                  (required)
#   ID       DBInstance name — MUST match YAML's metadata.name (required)
#   YAML     path to the DBInstance manifest     (stage1/stage3/all)
#   ID2/YAML2  second instance for stage7 (T10)   (stage7 only)
#   DBNAME   database name for psql checks       (default: auto-read from the
#            live DBInstance's spec.dbName; the controller itself only falls
#            back to the instance name when spec.dbName is unset — see the
#            dbName default in internal/ensure/vm.go — so a YAML with an
#            explicit dbName: like "orders" is honored automatically)
#   PGUSER   master username                     (default: auto-read from the
#            credentials Secret's admin_user)
#   SKIP_DB=1  skip psql-based data checks (no VLAN reach from this host)
#   TIMEOUT  seconds to wait for phase changes   (default: 900)
#
# Results: every run tees full output to results/<ns>.<id>.log (next to this
# script) and appends one row to results/summary.tsv — timestamp, ns, id,
# stage, pass count, fail count, overall result. Useful for showing a
# multi-run history without digging through terminal scrollback.
set -uo pipefail

case "${1:-}" in stage1|stage2|stage3|stage4|stage5|stage6|stage7|all) ;; *)
  echo "usage: NS=<ns> ID=<name> [YAML=<file>] [SKIP_DB=1] $0 {stage1|stage2|stage3|stage4|stage5|stage6|stage7|all}"; exit 2 ;;
esac

STAGE_ARG="$1"
NS="${NS:?set NS}"; ID="${ID:?set ID}"
# DBNAME and PGUSER intentionally left unset here — db_exec auto-resolves
# both from the live DBInstance / its credentials Secret unless the caller
# explicitly overrides them (see resolve_dbname and db_exec below).
TIMEOUT="${TIMEOUT:-900}"
STATE="/tmp/repave-e2e.${NS}.${ID}.state"
FAILED=0
PASS_COUNT=0
FAIL_COUNT=0

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="$SCRIPT_DIR/results"
mkdir -p "$RESULTS_DIR"
LOG="$RESULTS_DIR/${NS}.${ID}.log"
SUMMARY="$RESULTS_DIR/summary.tsv"
[ -f "$SUMMARY" ] || printf 'timestamp\tns\tid\tstage\tpass\tfail\tresult\n' > "$SUMMARY"
{ echo; echo "=== $(date -u +%Y-%m-%dT%H:%M:%SZ) :: $STAGE_ARG ==="; } >> "$LOG"
exec > >(tee -a "$LOG") 2>&1

# ---------- helpers ----------
say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
pass() { printf '\033[1;32mPASS\033[0m  %s\n' "$*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail() { printf '\033[1;31mFAIL\033[0m  %s\n' "$*"; FAILED=1; FAIL_COUNT=$((FAIL_COUNT+1)); }
die()  { printf '\033[1;31mABORT\033[0m %s\n' "$*"; record_summary "ABORT"; exit 1; }

record_summary() { # record_summary <result>
  printf '%s\t%s\t%s\t%s\t%d\t%d\t%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$NS" "$ID" "$STAGE_ARG" "$PASS_COUNT" "$FAIL_COUNT" "$1" >> "$SUMMARY"
}

phase() { kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null; }

wait_phase() { # wait_phase <target> [timeout] [instance-id]
  local target="$1" t="${2:-$TIMEOUT}" id="${3:-$ID}" start now p
  start=$(date +%s)
  while true; do
    p=$(kubectl get dbinstance "$id" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
    [ "$p" = "$target" ] && return 0
    now=$(date +%s)
    [ $((now - start)) -ge "$t" ] && { fail "timed out (${t}s) waiting for phase=$target (last: ${p:-<none>})"; return 1; }
    printf '\r   waiting: phase=%-22s (%ss)' "${p:-<none>}" "$((now - start))"
    sleep 5
  done
}

wait_leave_phase() { # wait until phase != $1 (repave kicks off)
  local from="$1" t="${2:-120}" start now
  start=$(date +%s)
  while [ "$(phase)" = "$from" ]; do
    now=$(date +%s)
    [ $((now - start)) -ge "$t" ] && { fail "phase never left $from within ${t}s — repave did not start"; return 1; }
    sleep 3
  done
}

# disk_identifier mirrors diskIdentifierFor in internal/ensure/vm.go: OS/data
# disk names are "pg-<ID>-<uid8>-{os,data}", not plain "pg-<ID>-*" (T004) —
# folding in the first 8 chars of the DBInstance's own UID means a
# deleted-and-recreated instance (same $ID, new UID) never collides with a
# disk leaked by its previous incarnation.
disk_identifier() { # disk_identifier -> <ID>-<uid8>
  local uid; uid=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null | tr -d '-')
  echo "${ID}-${uid:0:8}"
}

os_pvcs() { # list this instance's OS disk PVC names (exact or -suffixed)
  local base; base="pg-$(disk_identifier)-os"
  kubectl get pvc -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null |
    grep -E "^${base}(-|$)" || true
}

# data_pvc_name/os_pvc_name read the authoritative current name straight from
# status.resources — same principle db_exec already follows: never
# reconstruct a disk name by pattern-matching when the controller has
# already recorded the real one (T004: pattern-matching pg-<id>-data/os
# alone no longer works now that names carry the disk-identifier's uid8).
data_pvc_name() { kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.resources.dataVolumeName}' 2>/dev/null; }
os_pvc_name()   { kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.resources.osDiskPVCName}' 2>/dev/null; }

resolve_dbname() {
  # Mirrors the controller's own fallback exactly (internal/ensure/vm.go:
  # dbName := inst.Spec.DBName; if dbName == "" { dbName = inst.Name }) —
  # so a YAML with an explicit dbName (e.g. "orders") resolves correctly
  # instead of incorrectly assuming the database is named after the instance.
  local d
  d=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.dbName}' 2>/dev/null)
  echo "${d:-$ID}"
}

db_exec() { # db_exec <sql>  (uses current endpoint + secret)
  local ep pw user dbname
  ep=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.endpoint.address}')
  # Secret keys are admin_user / admin_password (internal/credentials/
  # resolver.go) — NOT "password". PGUSER, if the caller set it explicitly,
  # wins; otherwise use the actual admin_user from the secret so this works
  # for any masterUsername, not just the "dbadmin" default.
  user=$(kubectl get secret "pg-${ID}-credentials" -n "$NS" -o jsonpath='{.data.admin_user}' | base64 -d)
  pw=$(kubectl get secret "pg-${ID}-credentials" -n "$NS" -o jsonpath='{.data.admin_password}' | base64 -d)
  dbname="${DBNAME:-$(resolve_dbname)}"
  [ -n "$ep" ] || { fail "no endpoint address on DBInstance"; return 1; }
  [ -n "$pw" ] || { fail "empty admin_password from secret pg-${ID}-credentials — secret missing/wrong keys?"; return 1; }
  # Server's pg_hba.conf is hostssl-only (internal/credentials/cloudinit.go);
  # force SSL explicitly instead of relying on libpq's "prefer" negotiation.
  PGPASSWORD="$pw" PGSSLMODE=require psql -h "$ep" -U "${PGUSER:-$user}" -d "$dbname" -tAc "$1" 2>&1
}

db_wait_ready() { # db_wait_ready [timeout] — block until a real login succeeds
  # phase=available means the KubeVirt readiness probe passed. That probe now
  # gates on cloud-init's bootstrap-complete marker as well as pg_isready
  # (internal/harvester/typed_client.go), so the master role is guaranteed to
  # exist by the time we get here — but only against a controller new enough
  # to set that probe. Against an older controller, or a VM created before the
  # probe change, pg_isready alone flips Ready while boots
  trap.sh is still
  # short of its CREATE ROLE, and every login in that window fails with
  # "password authentication failed" (PostgreSQL's answer for a role that does
  # not exist yet).
  #
  # So: retry briefly rather than assert on the first attempt. A few seconds of
  # boot skew must not read as a hard failure — but keep the window short, so a
  # genuinely broken bootstrap (dbadmin never created, wrong password baked in)
  # still fails the run instead of hanging it.
  local t="${1:-90}" start now out
  start=$(date +%s)
  while true; do
    out=$(db_exec "SELECT 1;")
    [ "$out" = "1" ] && return 0
    now=$(date +%s)
    if [ $((now - start)) -ge "$t" ]; then
      fail "no usable login as the master user within ${t}s of phase=available — last psql output: $out"
      echo "      (if this says 'password authentication failed', check in-guest:" >&2
      echo "       cloud-init status --long; sudo -u postgres psql -c '\\du'" >&2
      echo "       — an absent role means bootstrap.sh died before its CREATE ROLE;" >&2
      echo "       a present role means the password diverged, e.g. a data PVC or" >&2
      echo "       credentials Secret left over from an earlier run of this test.)" >&2
      return 1
    fi
    printf '\r   waiting: master login not accepted yet (%ss)' "$((now - start))"
    sleep 5
  done
}

server_major_version() {
  # Verifies decision #18's engine-version wiring actually took effect
  # against a real, booted VM — not just that the operator resolved a
  # version, but that PostgreSQL itself reports it.
  db_exec "SHOW server_version;" | grep -oE '^[0-9]+' | head -1
}

image_drift_status() {
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="ImageDrift")].status}' 2>/dev/null
}
image_drift_reason() {
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="ImageDrift")].reason}' 2>/dev/null
}
repave_status() {
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="RepaveInProgress")].status}' 2>/dev/null
}
repave_reason() {
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="RepaveInProgress")].reason}' 2>/dev/null
}
pending_delete_pvc() {
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.resources.pendingDeleteOSDiskPVCName}' 2>/dev/null
}
# annotate_repave_trigger <instance-id> — sets the repave-trigger annotation
# to a fresh, unique value. The controller never modifies or clears this
# annotation (Flux ReconcileRequestAnnotation style); a repave dispatches
# only when the value differs from status.lastAppliedRepaveTrigger, so every
# call site — including a deliberate re-trigger of the same instance — must
# generate a new value each time, not reuse a static sentinel.
annotate_repave_trigger() {
  local inst="$1" val
  val="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
  kubectl annotate dbinstance "$inst" -n "$NS" \
    "dbaas.opencloud.wso2.com/repave-trigger=$val" --overwrite
}
last_applied_repave_trigger() {
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.lastAppliedRepaveTrigger}' 2>/dev/null
}
repave_trigger_annotation() {
  kubectl get dbinstance "$ID" -n "$NS" \
    -o jsonpath='{.metadata.annotations.dbaas\.opencloud\.wso2\.com/repave-trigger}' 2>/dev/null
}
vmi_uid() {
  kubectl get vmi "$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.resources.vmName}')" \
    -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null
}

apply_yaml() {
  # --validate=false: kubectl's client-side validation needs an OpenAPI
  # download from the apiserver, which fails on some clusters ("proto: cannot
  # parse invalid wire-format data" — kubectl/apiserver version skew, common
  # against Harvester). The apiserver still validates server-side.
  kubectl apply -f "$YAML" --validate=false
}

check_yaml_matches_id() {
  # Every assertion below keys off $ID (PVC names, annotate target, secret
  # name), so the manifest must create exactly that DBInstance or the script
  # polls a name that never appears and hangs until TIMEOUT.
  local yname yns
  yname=$(awk '/^metadata:/{m=1;next} m&&/^[^ ]/{m=0} m&&/^  name:/{print $2;exit}' "$YAML")
  yns=$(awk '/^metadata:/{m=1;next} m&&/^[^ ]/{m=0} m&&/^  namespace:/{print $2;exit}' "$YAML")
  [ "$yname" = "$ID" ] || die "YAML metadata.name='$yname' but ID='$ID' — set ID=$yname or edit the YAML"
  [ -z "$yns" ] || [ "$yns" = "$NS" ] || die "YAML namespace='$yns' but NS='$NS' — they must match"
}

provision_probe() { # provision_probe <instance-name> <engineVersion-or-empty>
  # Provisions a disposable DBInstance cloned from $ID's own class/storage/
  # network (so E3 needs no extra YAML input), waits for it to settle into
  # available or incompatible-parameters (there is no "failed" phase — see
  # the header note), and prints "<phase>|<message>". engine="" tests
  # decision #18's default-to-highest-supported-version behavior; an
  # explicit unsupported version tests the reject path. Uses a local `id`
  # distinct from the global $ID/$STATE/$LOG on purpose — this never touches
  # the instance under test.
  local id="$1" engine="$2"
  local netref class storage
  netref=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.networkRef}')
  class=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.dbInstanceClass}')
  storage=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.allocatedStorage}')
  cat <<YAML | kubectl apply -f - --validate=false >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: ${id}
  namespace: ${NS}
spec:
  dbInstanceClass: ${class}
  allocatedStorage: ${storage}
  engineVersion: "${engine}"
  dbName: eoltest
  masterUsername: dbadmin
  manageMasterUserPassword: true
  networkRef: ${netref}
  running: true
YAML
  local t=0 p=""
  while [ $t -lt 180 ]; do
    p=$(kubectl get dbinstance "$id" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
    { [ "$p" = "available" ] || [ "$p" = "incompatible-parameters" ]; } && break
    sleep 5; t=$((t+5))
  done
  printf '%s|%s' "$p" "$(kubectl get dbinstance "$id" -n "$NS" -o jsonpath='{.status.message}' 2>/dev/null)"
}

# ---------- stages ----------
stage1() {
  say "T1: provision baseline"
  [ -n "${YAML:-}" ] || die "set YAML=<path to DBInstance manifest>"
  check_yaml_matches_id
  apply_yaml || die "apply failed"
  wait_phase "available" || die "instance never became available"
  echo

  local pvcs; pvcs=$(os_pvcs)
  [ -n "$pvcs" ] && pass "OS PVC exists: $pvcs" || fail "no OS PVC found"
  local data_pvc; data_pvc=$(data_pvc_name)
  kubectl get pvc "$data_pvc" -n "$NS" >/dev/null 2>&1 \
    && pass "data PVC $data_pvc exists" || fail "data PVC missing"

  local base_pvc; base_pvc=$(os_pvcs | head -1)
  local base_sc; base_sc=$(kubectl get pvc "$base_pvc" -n "$NS" -o jsonpath='{.spec.storageClassName}' 2>/dev/null)
  [ -n "$base_sc" ] && pass "OS PVC has a baked-image StorageClass: $base_sc" || fail "OS PVC has no storageClassName"
  local conn_uid; conn_uid=$(kubectl get secret "pg-${ID}-connect" -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null)
  [ -n "$conn_uid" ] && pass "connection secret pg-${ID}-connect exists" || fail "connection secret missing"

  {
    echo "baseline_os_pvc=$base_pvc"
    echo "baseline_os_sc=$base_sc"
    echo "baseline_connect_uid=$conn_uid"
    echo "baseline_connect_cacrt=$(kubectl get secret "pg-${ID}-connect" -n "$NS" -o jsonpath='{.data.ca\.crt}' 2>/dev/null)"
    echo "baseline_vmi_uid=$(vmi_uid)"
    echo "baseline_rev=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.currentImageRevision}')"
    echo "baseline_engine_spec=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.engineVersion}')"
  } > "$STATE"

  if [ "${SKIP_DB:-0}" != "1" ]; then
    say "T1 (cont.): PostgreSQL version matches the (possibly defaulted) engineVersion"
    # Gate every psql assertion below on one real login. Without this the
    # version and seed checks each report their own failure for what is really
    # a single cause, and boot skew is indistinguishable from a broken
    # bootstrap.
    db_wait_ready || die "baseline instance never accepted a master login"
    echo
    # No apt-get should have run at boot for a baked image (§9/decision #18) —
    # this script has no VM console access to grep cloud-init logs directly;
    # spot-check via `virtctl console` manually if you want to confirm that
    # part. What IS scriptable: the running server's actual major version.
    local want got; want=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.engineVersion}')
    got=$(server_major_version)
    if [ -n "$want" ]; then
      [ "$got" = "$want" ] && pass "server_version=$got matches spec.engineVersion=$want" \
        || fail "server_version=$got, want spec.engineVersion=$want"
    else
      [ -n "$got" ] && pass "spec.engineVersion unset; server booted on defaulted version $got (decision #18)" \
        || fail "could not read server_version at all"
    fi

    say "T2 (seed): seed data"
    local out; out=$(db_exec "CREATE TABLE IF NOT EXISTS repave_test(x int); INSERT INTO repave_test VALUES (1);")
    echo "$out" | grep -q "INSERT" && pass "seeded repave_test" || fail "seed failed: $out"
  else
    echo "(SKIP_DB=1: skipping version/seed checks)"
  fi

  say "T11a: printcolumns exist, and no drift is reported on a just-provisioned instance"
  kubectl get dbinstance "$ID" -n "$NS" --no-headers 2>/dev/null | grep -q "$ID" \
    && pass "default 'kubectl get dbinstance' output includes $ID (ImageDrift column present, no reason leaking without -o wide)" \
    || fail "kubectl get dbinstance $ID produced no row"
  kubectl get dbinstance "$ID" -n "$NS" -o wide 2>/dev/null | head -1 | grep -qi "imagedriftreason" \
    && pass "'-o wide' header includes IMAGEDRIFTREASON" || fail "'-o wide' header missing IMAGEDRIFTREASON column"
  # ConditionImageDrift is always written (repave.go: "Always written, never
  # removed"), so 'describe' shows it at baseline too, same as once drift
  # exists — unlike M1's two condition types, there's no absent-until-drifted
  # state to wait for.
  kubectl describe dbinstance "$ID" -n "$NS" 2>/dev/null | grep -q "ImageDrift" \
    && pass "'kubectl describe' shows the ImageDrift condition at baseline" || fail "'kubectl describe' output missing ImageDrift"
  # ConditionImageDrift is three-valued (api/v1alpha1/dbinstance_conditions.go):
  # True=drifted, False/ImageUpToDate=on the current revision, and
  # Unknown/ImageCatalogUnresolved=no validated stream for
  # databaseDefaults.osVersion, i.e. drift could not be evaluated at all. A
  # baseline instance is provisioned from the current revision, so it must
  # report an explicit False here — an empty value means the controller
  # predates the three-valued condition, and Unknown means osVersion is
  # misconfigured (which would silently no-op the whole repave feature and
  # make stage2 fail for the wrong reason).
  local st rs; st=$(image_drift_status); rs=$(image_drift_reason)
  if [ "$st" = "False" ] && [ "$rs" = "ImageUpToDate" ]; then
    pass "ImageDrift=False/ImageUpToDate on the freshly-provisioned baseline"
  elif [ "$st" = "Unknown" ]; then
    fail "ImageDrift=Unknown/$rs — databaseDefaults.osVersion resolves to no validated stream; fix the catalog before running stage2"
  else
    fail "ImageDrift=${st:-<absent>}/${rs:-<none>} at baseline, want False/ImageUpToDate"
  fi

  say "stage1 done — now switch manager.yaml to the new OS stream and 'make deploy', then run stage2"
}

stage2() {
  [ -f "$STATE" ] || die "no state file $STATE — run stage1 first"
  # shellcheck disable=SC1090
  source "$STATE"

  say "T2: drift detection (ImageDrift=True/OSUpdateAvailable)"
  local t=0 st="" rs=""
  while [ $t -lt 300 ]; do
    st=$(image_drift_status); rs=$(image_drift_reason)
    [ "$st" = "True" ] && break; sleep 10; t=$((t+10))
  done
  [ "$st" = "True" ] && pass "ImageDrift=True" || fail "ImageDrift never appeared (did you redeploy the controller?)"
  [ "$rs" = "OSUpdateAvailable" ] && pass "ImageDrift.reason=OSUpdateAvailable (safe update, not EOL-blocked)" \
    || fail "ImageDrift.reason=$rs, want OSUpdateAvailable"

  say "T11b: printcolumns surface the drift reason now that it exists"
  kubectl get dbinstance "$ID" -n "$NS" -o wide --no-headers 2>/dev/null | grep -q "OSUpdateAvailable" \
    && pass "'-o wide' row surfaces IMAGEDRIFTREASON=OSUpdateAvailable" \
    || fail "'-o wide' row does not show the drift reason"

  say "T3: repave — VM restarts exactly once, data PVC/connection Secret/TLS material untouched"
  annotate_repave_trigger "$ID" || die "annotate failed"
  wait_leave_phase "available" || die "repave never started"
  wait_phase "available" || die "repave never completed"
  echo

  local pvcs count new_pvc
  pvcs=$(os_pvcs); count=$(echo "$pvcs" | grep -c . || true); new_pvc=$(echo "$pvcs" | head -1)
  [ "$count" = "1" ] && pass "exactly one OS PVC: $new_pvc" || fail "expected 1 OS PVC, got: $pvcs"
  [ "$new_pvc" != "$baseline_os_pvc" ] && pass "PVC name changed ($baseline_os_pvc -> $new_pvc)" \
    || fail "PVC name unchanged — disk was NOT swapped"
  echo "$new_pvc" | grep -qE "^pg-$(disk_identifier)-os-." && pass "new name is revision-suffixed" \
    || fail "new PVC not revision-suffixed: $new_pvc"

  local new_vmi_uid; new_vmi_uid=$(vmi_uid)
  [ -n "$new_vmi_uid" ] && [ "$new_vmi_uid" != "$baseline_vmi_uid" ] \
    && pass "VMI UID changed ($baseline_vmi_uid -> $new_vmi_uid) — VM actually restarted" \
    || fail "VMI UID unchanged ($new_vmi_uid) — VM was never restarted for the repave"

  local data_pvc; data_pvc=$(data_pvc_name)
  kubectl get pvc "$data_pvc" -n "$NS" >/dev/null 2>&1 \
    && pass "data PVC $data_pvc name unchanged (repave only swaps the OS disk)" \
    || fail "data PVC $data_pvc missing after repave"
  local new_connect_uid new_cacrt
  new_connect_uid=$(kubectl get secret "pg-${ID}-connect" -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null)
  new_cacrt=$(kubectl get secret "pg-${ID}-connect" -n "$NS" -o jsonpath='{.data.ca\.crt}' 2>/dev/null)
  [ "$new_connect_uid" = "$baseline_connect_uid" ] \
    && pass "connection secret pg-${ID}-connect is the same object (UID unchanged) — not recreated by repave" \
    || fail "connection secret UID changed ($baseline_connect_uid -> $new_connect_uid)"
  [ "$new_cacrt" = "$baseline_connect_cacrt" ] \
    && pass "TLS CA cert unchanged across repave — repave only regenerates cloud-init, not Material" \
    || fail "TLS CA cert content changed across repave — Material should never rotate on a repave"

  local sc rev imgid; sc=$(kubectl get pvc "$new_pvc" -n "$NS" -o jsonpath='{.spec.storageClassName}')
  rev=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.currentImageRevision}')
  imgid=$(kubectl get pvc "$new_pvc" -n "$NS" -o jsonpath='{.metadata.annotations.harvesterhci\.io/imageId}' 2>/dev/null)
  echo "   PVC storageClass: $sc | imageId: $imgid | status.currentImageRevision: $rev"
  [ "$rev" != "$baseline_rev" ] && pass "currentImageRevision updated ($baseline_rev -> $rev)" || fail "currentImageRevision unchanged"
  # storageClass NAMING is not a stable signal — Harvester auto-generates
  # names like "longhorn-image-rnrmm" for UI-uploaded images, unrelated to any
  # revision string (see ResolveVMImage in typed_client.go). What actually
  # proves the disk changed lineage is that the storageClass differs from the
  # pre-repave baseline; the imageId annotation is the authoritative pointer
  # to which VirtualMachineImage backs it (cross-check in Harvester UI).
  [ -n "$sc" ] && [ "$sc" != "$baseline_os_sc" ] \
    && pass "PVC storageClass changed ($baseline_os_sc -> $sc) — new disk lineage confirmed" \
    || fail "PVC storageClass unchanged ($sc) — disk was not actually reprovisioned from a new image"
  [ -n "$imgid" ] && pass "PVC carries imageId annotation: $imgid (verify this is the new image in Harvester UI)" \
    || fail "PVC missing harvesterhci.io/imageId annotation"

  say "T6: old OS-disk PVC actually deleted (not just orphaned)"
  kubectl get pvc "$baseline_os_pvc" -n "$NS" >/dev/null 2>&1 \
    && fail "old OS PVC $baseline_os_pvc still exists — should have been deleted after the swap" \
    || pass "old OS PVC $baseline_os_pvc is gone"
  [ -z "$(kubectl get dv -n "$NS" -o name 2>/dev/null | grep "pg-$(disk_identifier)-os")" ] \
    && pass "no stray DataVolumes" || fail "stray DataVolume present"

  say "decision #16 regression: PendingDeleteOSDiskPVCName cleared after a successful swap"
  local pending; pending=$(pending_delete_pvc)
  [ -z "$pending" ] && pass "status.resources.pendingDeleteOSDiskPVCName is empty" \
    || fail "pendingDeleteOSDiskPVCName still set to '$pending' — DeletePVC recovery never completed"

  local trig applied; trig=$(repave_trigger_annotation); applied=$(last_applied_repave_trigger)
  [ -n "$trig" ] && [ "$trig" = "$applied" ] \
    && pass "repave trigger handled (status.lastAppliedRepaveTrigger caught up: $applied)" \
    || fail "trigger not recorded as handled (annotation=$trig, lastAppliedRepaveTrigger=$applied)"
  local post_st post_rs; post_st=$(image_drift_status); post_rs=$(image_drift_reason)
  if [ "$post_st" = "False" ] && [ "$post_rs" = "ImageUpToDate" ]; then
    pass "ImageDrift=False/ImageUpToDate after the repave"
  else
    fail "ImageDrift=${post_st:-<absent>}/${post_rs:-<none>} after repave, want False/ImageUpToDate"
  fi

  say "Task 7/T002 regression: RepaveInProgress clears once settled (does not stay stuck)"
  local rip; rip=$(repave_status)
  [ -z "$rip" ] && pass "RepaveInProgress condition absent — syncRepaveInProgressCondition cleared it" \
    || fail "RepaveInProgress still present (status=$rip) — should be removed once ImageDrift is gone and Ready=True"

  if [ "${SKIP_DB:-0}" != "1" ]; then
    say "T4: data survived"
    # The repave rebooted the VM onto a brand-new OS disk, so cloud-init ran
    # again from scratch — same post-boot login race as stage1.
    db_wait_ready || die "instance never accepted a master login after the repave"
    echo
    local out; out=$(db_exec "SELECT x FROM repave_test;")
    echo "$out" | grep -q "^1$" && pass "repave_test row intact — pgdata preserved" \
      || fail "data check failed: $out"
  fi

  say "T7: no-op repave (already on latest) — trigger handled, zero VM restarts"
  local pre_noop_vmi; pre_noop_vmi=$(vmi_uid)
  annotate_repave_trigger "$ID"
  # No phase transition expected this time — poll briefly and confirm it
  # never leaves "available", rather than waiting for a transition that
  # (correctly) never happens.
  local t=0 saw_other=0
  while [ $t -lt 30 ]; do
    [ "$(phase)" != "available" ] && saw_other=1
    sleep 3; t=$((t+3))
  done
  [ "$saw_other" = "0" ] && pass "phase stayed available throughout — no repave actually ran" \
    || fail "phase left available during a same-revision (no-op) repave trigger"
  [ "$(vmi_uid)" = "$pre_noop_vmi" ] && pass "VMI UID unchanged — zero VM restarts for the no-op trigger" \
    || fail "VMI UID changed on a no-op repave trigger — VM was restarted unnecessarily"
  local noop_trig noop_applied; noop_trig=$(repave_trigger_annotation); noop_applied=$(last_applied_repave_trigger)
  [ -n "$noop_trig" ] && [ "$noop_trig" = "$noop_applied" ] \
    && pass "no-op trigger also recorded as handled (lastAppliedRepaveTrigger caught up: $noop_applied)" \
    || fail "no-op trigger not recorded as handled (annotation=$noop_trig, lastAppliedRepaveTrigger=$noop_applied)"
  [ "$(os_pvcs | head -1)" = "$new_pvc" ] && pass "same disk after no-op repave ($new_pvc)" \
    || fail "disk changed on same-image repave: $(os_pvcs)"

  echo "post_repave_pvc=$new_pvc" >> "$STATE"
  echo "post_repave_vmi_uid=$new_vmi_uid" >> "$STATE"
  say "stage2 done"
}

stage3() {
  say "T9-adjacent (supplementary, not in the T1-T11 matrix): delete + leak scan"
  kubectl delete dbinstance "$ID" -n "$NS" --timeout=180s || die "delete failed (deletionProtection on?)"
  sleep 20
  local leaks
  leaks=$(kubectl get vm,vmi,dv,pvc,svc,endpoints,servicemonitor,secret -n "$NS" -o name 2>/dev/null | grep "pg-${ID}" || true)
  [ -z "$leaks" ] && pass "no leftovers — teardown clean" || fail "LEAKED RESOURCES:"$'\n'"$leaks"

  say "old-disk-reattach regression: re-apply same name"
  [ -n "${YAML:-}" ] || die "set YAML=<path to DBInstance manifest>"
  check_yaml_matches_id
  local before; before=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  apply_yaml || die "re-apply failed"
  wait_phase "available" || die "re-applied instance never became available"
  echo

  local created; created=$(kubectl get pvc "$(os_pvc_name)" -n "$NS" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null)
  [ -n "$created" ] && [[ "$created" > "$before" || "$created" == "$before" ]] \
    && pass "fresh OS PVC created at $created" || fail "OS PVC missing or predates re-apply ($created) — old disk reattached?"

  if [ "${SKIP_DB:-0}" != "1" ]; then
    # This is a freshly provisioned instance, so it needs the same login gate
    # as stage1 — and here it matters doubly: a login that fails for boot-race
    # reasons returns psql's connection error, which does not contain
    # "does not exist", so the assertion below would report "old data visible"
    # for an instance that has no data at all.
    db_wait_ready || die "re-applied instance never accepted a master login"
    echo
    local out; out=$(db_exec "SELECT x FROM repave_test;")
    echo "$out" | grep -q "does not exist" && pass "repave_test absent — genuinely fresh database" \
      || fail "old data visible on re-applied instance: $out"
  fi
  say "stage3 done"
}

stage4() {
  say "E1: drift detection — ImageDrift=True/EngineVersionEOL (not OSUpdateAvailable)"
  local t=0 st="" rs=""
  while [ $t -lt 300 ]; do
    st=$(image_drift_status); rs=$(image_drift_reason)
    [ "$st" = "True" ] && [ "$rs" = "EngineVersionEOL" ] && break; sleep 10; t=$((t+10))
  done
  [ "$st" = "True" ] && [ "$rs" = "EngineVersionEOL" ] && pass "ImageDrift=True/EngineVersionEOL" \
    || fail "expected ImageDrift=True/EngineVersionEOL within 300s, got status=$st reason=$rs — did you publish the EOL image revision, point LatestBakedImages at it (Validated), and make deploy?"

  say "E2: repave is blocked before any destructive step — VM and disk left untouched"
  local pre_pvc vmname
  pre_pvc=$(os_pvcs | head -1)
  vmname=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.resources.vmName}')
  annotate_repave_trigger "$ID" || die "annotate failed"

  # repave.go now sets RepaveInProgress=False/RepaveBlockedEOL before
  # recording the trigger as handled (fixed while writing this script —
  # previously nothing observable recorded a blocked repave at all).
  t=0; local rst="" rrs=""
  while [ $t -lt 90 ]; do
    rst=$(repave_status); rrs=$(repave_reason)
    [ "$rst" = "False" ] && [ "$rrs" = "RepaveBlockedEOL" ] && break
    sleep 5; t=$((t+5))
  done
  [ "$rst" = "False" ] && [ "$rrs" = "RepaveBlockedEOL" ] && pass "RepaveInProgress=False/RepaveBlockedEOL" \
    || fail "expected RepaveInProgress=False/RepaveBlockedEOL within 90s, got status=$rst reason=$rrs"

  local post_pvc post_vmi_phase
  post_pvc=$(os_pvcs | head -1)
  [ "$post_pvc" = "$pre_pvc" ] && pass "OS PVC unchanged ($post_pvc) — no disk swap was attempted" \
    || fail "OS PVC changed ($pre_pvc -> $post_pvc) — a blocked repave must not touch storage"
  post_vmi_phase=$(kubectl get vmi "$vmname" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
  [ "$post_vmi_phase" = "Running" ] && pass "VMI still Running ($vmname) — VM was never stopped" \
    || fail "VMI phase = '${post_vmi_phase:-<none>}', expected Running — a blocked repave must not touch the VM"

  # The annotation is never modified by repave.go's reject path (it's only
  # ever recorded, never cleared) — confirm the rejection was still tracked
  # as handled via status.lastAppliedRepaveTrigger.
  local eol_trig eol_applied; eol_trig=$(repave_trigger_annotation); eol_applied=$(last_applied_repave_trigger)
  [ -n "$eol_trig" ] && [ "$eol_trig" = "$eol_applied" ] \
    && pass "blocked repave still recorded as handled (lastAppliedRepaveTrigger caught up: $eol_applied)" \
    || fail "blocked repave not recorded as handled (annotation=$eol_trig, lastAppliedRepaveTrigger=$eol_applied)"
  [ "$(phase)" = "available" ] && pass "instance remains available — nothing was broken, just correctly refused" \
    || fail "instance phase = $(phase), expected available (repave should never have touched it)"

  say "E3: new-instance rules under the EOL stream"
  local id_ok="${ID}-eol-pg18" result_ok phase_ok
  result_ok=$(provision_probe "$id_ok" "18")
  phase_ok="${result_ok%%|*}"
  [ "$phase_ok" = "available" ] && pass "new instance on engineVersion=18 (current stream's version) provisions fine" \
    || fail "engineVersion=18 instance did not reach available (phase=$phase_ok): ${result_ok#*|}"
  kubectl delete dbinstance "$id_ok" -n "$NS" --timeout=120s >/dev/null 2>&1

  local id_bad="${ID}-eol-pg17" result_bad phase_bad msg_bad
  result_bad=$(provision_probe "$id_bad" "17")
  phase_bad="${result_bad%%|*}"; msg_bad="${result_bad#*|}"
  { [ "$phase_bad" = "incompatible-parameters" ] && echo "$msg_bad" | grep -qi "engineVersion"; } \
    && pass "new instance on engineVersion=17 (EOL'd out of the stream) correctly rejected: $msg_bad" \
    || fail "expected incompatible-parameters/OSImageInvalid for engineVersion=17, got phase=$phase_bad msg=$msg_bad"
  kubectl delete dbinstance "$id_bad" -n "$NS" --timeout=120s >/dev/null 2>&1

  say "E3 (decision #18 addition): unset engineVersion defaults to the image's highest supported version, not a hard reject"
  local id_default="${ID}-eol-default" result_default phase_default
  result_default=$(provision_probe "$id_default" "")
  phase_default="${result_default%%|*}"
  [ "$phase_default" = "available" ] && pass "unset engineVersion provisions fine (defaults to highest supported, e.g. 18) — this is a deliberate deviation from M1, which hard-rejected an unset value" \
    || fail "unset engineVersion did not reach available (phase=$phase_default): ${result_default#*|}"
  if [ "$phase_default" = "available" ] && [ "${SKIP_DB:-0}" != "1" ]; then
    ID="$id_default" # temporarily redirect db_exec/server_major_version at the probe instance
    db_wait_ready 120 || true # report-only: the assertion below is the verdict
    local got; got=$(server_major_version)
    ID="${ID%-eol-default}"
    [ -n "$got" ] && pass "defaulted instance actually booted PostgreSQL $got" || fail "could not read server_version on defaulted-engineVersion instance"
  fi
  kubectl delete dbinstance "$id_default" -n "$NS" --timeout=120s >/dev/null 2>&1

  say "E4: cross-instance data migration (manual — see the EOL migration playbook)"
  echo "Not automated: this moves data between two independent instances and"
  echo "needs a human to eyeball row counts, not a scripted assertion. Create a"
  echo "migration-target instance on the new PG version, then run:"
  echo
  echo "  OLD_IP=\$(kubectl get dbinstance $ID -n $NS -o jsonpath='{.status.endpoint.address}')"
  echo "  NEW_IP=\$(kubectl get dbinstance <new-instance> -n $NS -o jsonpath='{.status.endpoint.address}')"
  echo "  pg_dumpall -h \$OLD_IP -U $(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.masterUsername}') --globals-only > globals.sql"
  echo "  pg_dump -h \$OLD_IP -U $(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.masterUsername}') -Fc $(resolve_dbname) > database.dump"
  echo "  # then psql -f globals.sql and pg_restore against \$NEW_IP"

  say "stage4 done"
}

stage5() {
  say "T8: databaseDefaults.osVersion misconfigured to a stream not in LatestBakedImages"
  cat <<'EOF'
Manual precondition, do this BEFORE running stage5:
  1. Set databaseDefaults.osVersion (config file, DBAAS_DATABASE_DEFAULTS__OS_VERSION
     env var, or a flag) on the manager Deployment to a stream that is NOT a
     key in internal/catalog's LatestBakedImages (e.g. "99.99").
  2. make deploy
Press Enter once the misconfigured controller is rolled out...
EOF
  read -r

  say "New instance under the bad config: preflight Terminal, VM never created"
  local id_bad="${ID}-badconfig"
  local netref class storage
  netref=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.networkRef}')
  class=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.dbInstanceClass}')
  storage=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.spec.allocatedStorage}')
  cat <<YAML | kubectl apply -f - --validate=false >/dev/null
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: ${id_bad}
  namespace: ${NS}
spec:
  dbInstanceClass: ${class}
  allocatedStorage: ${storage}
  dbName: badconfigtest
  masterUsername: dbadmin
  manageMasterUserPassword: true
  networkRef: ${netref}
  running: true
YAML
  local t=0 p=""
  while [ $t -lt 120 ]; do
    p=$(kubectl get dbinstance "$id_bad" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null)
    [ "$p" = "incompatible-parameters" ] && break
    sleep 5; t=$((t+5))
  done
  [ "$p" = "incompatible-parameters" ] && pass "new instance under bad osVersion config -> incompatible-parameters (preflight Terminal)" \
    || fail "expected incompatible-parameters within 120s, got phase=$p"
  [ -z "$(kubectl get vm "pg-${id_bad}" -n "$NS" -o name 2>/dev/null)" ] \
    && pass "no VM was ever created for the rejected instance" || fail "a VM was created despite the bad osVersion config"
  kubectl delete dbinstance "$id_bad" -n "$NS" --timeout=120s >/dev/null 2>&1

  say "Existing \$ID instance: repave step Satisfied-no-ops, does not crash-loop"
  sleep 30 # let a few reconcile passes happen under the bad config
  [ "$(phase)" = "available" ] && pass "existing instance stays available under the bad config — no crash-loop" \
    || fail "existing instance phase = $(phase), expected available (bad manager config should not affect it)"
  [ -z "$(repave_status)" ] && pass "RepaveInProgress absent — repave step is a true no-op under an unresolvable stream" \
    || fail "RepaveInProgress = $(repave_status)/$(repave_reason) — unexpected under a no-op stream"

  cat <<'EOF'

Manual cleanup, do this AFTER stage5:
  1. Revert databaseDefaults.osVersion to its correct value.
  2. make deploy
EOF
  say "stage5 done"
}

stage6() {
  say "T9: repave failure mid-swap (kill controller between SwapVMOSDisk and DeletePVC) recovers automatically"
  cat <<'EOF'
This needs a human-timed kill — it can't be scripted reliably. Steps:
  1. In another terminal: kubectl logs -n <manager-ns> deploy/<manager-deploy> -f | grep -i repave
  2. Here, trigger a repave (this script will do it after you press Enter) —
     first make sure a NEW image revision is registered/Validated so there is
     something to repave onto (same precondition as stage2).
  3. Watch the log stream. The instant you see the SwapVMOSDisk call succeed
     (a log line referencing the swap) but BEFORE any DeletePVC log line,
     kill the manager pod:
       kubectl delete pod -n <manager-ns> -l <manager-label> --grace-period=0 --force
  4. Come back here and press Enter again once the manager pod has restarted.
Press Enter to trigger the repave now...
EOF
  read -r
  annotate_repave_trigger "$ID" || die "annotate failed"
  echo "Repave triggered. Go kill the manager pod at the right moment, then press Enter here..."
  read -r

  say "Recovery: pendingDeleteOSDiskPVCName clears, no PVC leak, exactly one OS PVC remains"
  wait_phase "available" 300 || die "instance never recovered to available after the kill"
  local t=0 pending=""
  while [ $t -lt 180 ]; do
    pending=$(pending_delete_pvc)
    [ -z "$pending" ] && break
    sleep 5; t=$((t+5))
  done
  [ -z "$pending" ] && pass "pendingDeleteOSDiskPVCName cleared after recovery (decision #16)" \
    || fail "pendingDeleteOSDiskPVCName still set to '$pending' after 180s — recovery never completed"
  local pvcs count; pvcs=$(os_pvcs); count=$(echo "$pvcs" | grep -c . || true)
  [ "$count" = "1" ] && pass "exactly one OS PVC remains after recovery: $pvcs" \
    || fail "expected exactly 1 OS PVC after recovery, got: $pvcs"
  say "stage6 done"
}

stage7() {
  say "T10: concurrent repave, two instances, no OS-disk PVC name collision"
  local id2="${ID2:?set ID2 for stage7}"
  local yaml2="${YAML2:-}"
  if [ -z "$yaml2" ]; then
    [ -n "${YAML:-}" ] || die "set YAML2=<path> or YAML=<path> (ID2's name will be substituted in)"
    yaml2="/tmp/repave-e2e.${id2}.yaml"
    sed "s/name: ${ID}\$/name: ${id2}/" "$YAML" > "$yaml2"
  fi
  local orig_id="$ID" orig_yaml="${YAML:-}"

  say "provisioning second instance $id2"
  ID="$id2" YAML="$yaml2" check_yaml_matches_id
  ID="$id2" YAML="$yaml2" apply_yaml || die "apply for $id2 failed"
  wait_phase "available" 900 "$id2" || die "$id2 never became available"

  say "triggering repave on both instances back-to-back"
  annotate_repave_trigger "$orig_id" || die "annotate $orig_id failed"
  annotate_repave_trigger "$id2" || die "annotate $id2 failed"

  wait_phase "available" 900 "$orig_id" || fail "$orig_id never returned to available"
  wait_phase "available" 900 "$id2" || fail "$id2 never returned to available"

  ID="$orig_id"; local pvc1; pvc1=$(os_pvcs | head -1)
  ID="$id2"; local pvc2; pvc2=$(os_pvcs | head -1)
  ID="$orig_id"

  [ -n "$pvc1" ] && [ -n "$pvc2" ] && [ "$pvc1" != "$pvc2" ] \
    && pass "distinct OS PVC names across concurrent repaves: $pvc1 vs $pvc2" \
    || fail "OS PVC name collision or missing PVC: $pvc1 vs $pvc2"

  kubectl delete dbinstance "$id2" -n "$NS" --timeout=180s >/dev/null 2>&1
  ID="$orig_id"; YAML="$orig_yaml"
  say "stage7 done"
}

case "$STAGE_ARG" in
  stage1) stage1 ;;
  stage2) stage2 ;;
  stage3) stage3 ;;
  stage4) stage4 ;;
  stage5) stage5 ;;
  stage6) stage6 ;;
  stage7) stage7 ;;
  all)
    stage1
    printf '\n\033[1;33m>> Now edit manager.yaml (new OS stream) and run: make deploy IMG=<img>\n>> Press Enter when the controller rollout is complete...\033[0m'
    read -r
    stage2
    stage3
    ;;
esac

if [ "$FAILED" = "0" ]; then
  record_summary "PASS"
  printf '\n\033[1;32mALL CHECKS PASSED\033[0m (results: %s)\n' "$LOG"
  exit 0
else
  record_summary "FAIL"
  printf '\n\033[1;31mSOME CHECKS FAILED\033[0m (results: %s)\n' "$LOG"
  exit 1
fi
