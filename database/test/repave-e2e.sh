#!/usr/bin/env bash
# repave-e2e.sh — stage-based e2e test runner for DBaaS OS repave + teardown.
# See P012-repave-testing-guide.md for the test matrix (T1..T10).
#
# Usage:
#   NS=tenant-acme ID=test-repave YAML=./test.yaml ./repave-e2e.sh stage1
#   NS=tenant-acme ID=test-repave                  ./repave-e2e.sh stage2
#   NS=tenant-acme ID=test-repave YAML=./test.yaml ./repave-e2e.sh stage3
#   NS=... ID=... YAML=... ./repave-e2e.sh all     # pauses at controller redeploy
#
# Env:
#   NS       instance namespace                  (required)
#   ID       DBInstance name                     (required)
#   YAML     path to the DBInstance manifest     (stage1/stage3/all)
#   DBNAME   database name for psql checks       (default: value of ID)
#   PGUSER   master username                     (default: dbadmin)
#   SKIP_DB=1  skip psql-based data checks (no VLAN reach from this host)
#   TIMEOUT  seconds to wait for phase changes   (default: 900)
set -uo pipefail

case "${1:-}" in stage1|stage2|stage3|all) ;; *)
  echo "usage: NS=<ns> ID=<name> [YAML=<file>] [SKIP_DB=1] $0 {stage1|stage2|stage3|all}"; exit 2 ;;
esac

NS="${NS:?set NS}"; ID="${ID:?set ID}"
DBNAME="${DBNAME:-$ID}"; PGUSER="${PGUSER:-dbadmin}"
TIMEOUT="${TIMEOUT:-900}"
STATE="/tmp/repave-e2e.${NS}.${ID}.state"
ANNOT="dbaas.opencloud.wso2.com/repave-trigger=now"
FAILED=0

# ---------- helpers ----------
say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
pass() { printf '\033[1;32mPASS\033[0m  %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL\033[0m  %s\n' "$*"; FAILED=1; }
die()  { printf '\033[1;31mABORT\033[0m %s\n' "$*"; exit 1; }

phase() { kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.provisioningPhase}' 2>/dev/null; }

wait_phase() { # wait_phase <target> [timeout]
  local target="$1" t="${2:-$TIMEOUT}" start now p
  start=$(date +%s)
  while true; do
    p=$(phase)
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

os_pvcs() { # list this instance's OS disk PVC names (exact or -suffixed)
  kubectl get pvc -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null |
    grep -E "^pg-${ID}-os(-|$)" || true
}

db_exec() { # db_exec <sql>  (uses current endpoint + secret)
  local ep pw
  ep=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.endpoint.address}')
  pw=$(kubectl get secret "pg-${ID}-credentials" -n "$NS" -o jsonpath='{.data.password}' | base64 -d)
  [ -n "$ep" ] || { fail "no endpoint address on DBInstance"; return 1; }
  PGPASSWORD="$pw" psql -h "$ep" -U "$PGUSER" -d "$DBNAME" -tAc "$1" 2>&1
}

# ---------- stages ----------
stage1() {
  say "T1: provision baseline"
  [ -n "${YAML:-}" ] || die "set YAML=<path to DBInstance manifest>"
  kubectl apply -f "$YAML" || die "apply failed"
  wait_phase "available" || die "instance never became Available"
  echo

  local pvcs; pvcs=$(os_pvcs)
  [ -n "$pvcs" ] && pass "OS PVC exists: $pvcs" || fail "no OS PVC found"
  kubectl get pvc "pg-${ID}-data" -n "$NS" >/dev/null 2>&1 \
    && pass "data PVC pg-${ID}-data exists" || fail "data PVC missing"

  echo "baseline_os_pvc=$(os_pvcs | head -1)" > "$STATE"
  kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.appliedSpec.imageRevision}' \
    | xargs -I{} sh -c 'echo "baseline_rev={}" >> '"$STATE"

  if [ "${SKIP_DB:-0}" != "1" ]; then
    say "T2: seed data"
    local out; out=$(db_exec "CREATE TABLE IF NOT EXISTS repave_test(x int); INSERT INTO repave_test VALUES (1);")
    echo "$out" | grep -q "INSERT" && pass "seeded repave_test" || fail "seed failed: $out"
  else
    echo "(SKIP_DB=1: skipping seed)"
  fi

  say "stage1 done — now switch manager.yaml to the new OS stream and 'make deploy', then run stage2"
}

stage2() {
  [ -f "$STATE" ] || die "no state file $STATE — run stage1 first"
  # shellcheck disable=SC1090
  source "$STATE"

  say "T3: drift detection (OSUpdateAvailable)"
  local t=0 cond=""
  while [ $t -lt 300 ]; do
    cond=$(kubectl get dbinstance "$ID" -n "$NS" \
      -o jsonpath='{.status.conditions[?(@.type=="OSUpdateAvailable")].status}' 2>/dev/null)
    [ "$cond" = "True" ] && break; sleep 10; t=$((t+10))
  done
  [ "$cond" = "True" ] && pass "OSUpdateAvailable=True" || fail "OSUpdateAvailable never appeared (did you redeploy the controller?)"

  say "T4: repave"
  kubectl annotate dbinstance "$ID" -n "$NS" "$ANNOT" --overwrite || die "annotate failed"
  wait_leave_phase "Available" || die "repave never started"
  wait_phase "Available" || die "repave never completed"
  echo

  local pvcs count new_pvc
  pvcs=$(os_pvcs); count=$(echo "$pvcs" | grep -c . || true); new_pvc=$(echo "$pvcs" | head -1)
  [ "$count" = "1" ] && pass "exactly one OS PVC: $new_pvc" || fail "expected 1 OS PVC, got: $pvcs"
  [ "$new_pvc" != "$baseline_os_pvc" ] && pass "PVC name changed ($baseline_os_pvc → $new_pvc)" \
    || fail "PVC name unchanged — disk was NOT swapped"
  echo "$new_pvc" | grep -qE "^pg-${ID}-os-." && pass "new name is revision-suffixed" \
    || fail "new PVC not revision-suffixed: $new_pvc"

  local sc rev; sc=$(kubectl get pvc "$new_pvc" -n "$NS" -o jsonpath='{.spec.storageClassName}')
  rev=$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.appliedSpec.imageRevision}')
  echo "   PVC storageClass: $sc | appliedSpec.imageRevision: $rev"
  [ "$rev" != "$baseline_rev" ] && pass "imageRevision updated ($baseline_rev → $rev)" || fail "imageRevision unchanged"
  echo "$sc" | grep -q "$rev" && pass "PVC storageClass matches new revision" \
    || fail "PVC storageClass ($sc) doesn't reference new revision ($rev) — verify in Harvester UI"

  [ -z "$(kubectl get dv -n "$NS" -o name 2>/dev/null | grep "pg-${ID}-os")" ] \
    && pass "no stray DataVolumes" || fail "stray DataVolume present"
  [ -z "$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.metadata.annotations.dbaas\.opencloud\.wso2\.com/repave-trigger}')" ] \
    && pass "repave-trigger annotation cleared" || fail "annotation still present"
  [ "$(kubectl get dbinstance "$ID" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="OSUpdateAvailable")].status}')" != "True" ] \
    && pass "OSUpdateAvailable condition removed" || fail "OSUpdateAvailable still True"

  if [ "${SKIP_DB:-0}" != "1" ]; then
    say "T5: data survived"
    local out; out=$(db_exec "SELECT x FROM repave_test;")
    echo "$out" | grep -q "^1$" && pass "repave_test row intact — pgdata preserved" \
      || fail "data check failed: $out"
  fi

  say "T6: second repave (idempotency — this used to hang the VM)"
  kubectl annotate dbinstance "$ID" -n "$NS" "$ANNOT" --overwrite
  wait_leave_phase "Available" 180 && wait_phase "Available" || die "second repave hung — REGRESSION"
  echo
  [ "$(os_pvcs | head -1)" = "$new_pvc" ] && pass "same disk after second repave ($new_pvc)" \
    || fail "disk changed on same-image repave: $(os_pvcs)"

  echo "post_repave_pvc=$new_pvc" >> "$STATE"
  say "stage2 done"
}

stage3() {
  say "T9: delete + leak scan"
  kubectl delete dbinstance "$ID" -n "$NS" --timeout=180s || die "delete failed (deletionProtection on?)"
  sleep 20
  local leaks
  leaks=$(kubectl get vm,vmi,dv,pvc,svc,endpoints,servicemonitor,secret -n "$NS" -o name 2>/dev/null | grep "pg-${ID}" || true)
  [ -z "$leaks" ] && pass "no leftovers — teardown clean" || fail "LEAKED RESOURCES:"$'\n'"$leaks"

  say "T10: re-apply same name (old-disk-reattach regression)"
  [ -n "${YAML:-}" ] || die "set YAML=<path to DBInstance manifest>"
  local before; before=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  kubectl apply -f "$YAML" || die "re-apply failed"
  wait_phase "Available" || die "re-applied instance never became Available"
  echo

  local created; created=$(kubectl get pvc "pg-${ID}-os" -n "$NS" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null)
  [ -n "$created" ] && [[ "$created" > "$before" || "$created" == "$before" ]] \
    && pass "fresh OS PVC created at $created" || fail "OS PVC missing or predates re-apply ($created) — old disk reattached?"

  if [ "${SKIP_DB:-0}" != "1" ]; then
    local out; out=$(db_exec "SELECT x FROM repave_test;")
    echo "$out" | grep -q "does not exist" && pass "repave_test absent — genuinely fresh database" \
      || fail "old data visible on re-applied instance: $out"
  fi
  say "stage3 done"
}

case "$1" in
  stage1) stage1 ;;
  stage2) stage2 ;;
  stage3) stage3 ;;
  all)
    stage1
    printf '\n\033[1;33m>> Now edit manager.yaml (new OS stream) and run: make deploy IMG=<img>\n>> Press Enter when the controller rollout is complete...\033[0m'
    read -r
    stage2
    stage3
    ;;
esac

[ "$FAILED" = "0" ] && { printf '\n\033[1;32mALL CHECKS PASSED\033[0m\n'; exit 0; } \
  || { printf '\n\033[1;31mSOME CHECKS FAILED\033[0m\n'; exit 1; }
