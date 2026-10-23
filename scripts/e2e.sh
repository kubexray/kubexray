#!/usr/bin/env bash
# Break-it scenarios end to end: breaks the demo shop on kind in every way
# `make break` knows, and checks kubexray gives the expected diagnosis and
# cause for each. Uses the demo's own cluster and kubeconfig.
#
#   scripts/e2e.sh                 every scenario
#   scripts/e2e.sh oom bad-probe   only these
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KUBECONFIG="$ROOT/.demo/kubeconfig"
export BREAK_DELAY="${BREAK_DELAY:-5}" # the config scenario's wait before a pod is replaced
KX="$ROOT/bin/kubexray"
TIMEOUT="${E2E_TIMEOUT:-240}"
CLUSTER="${DEMO_CLUSTER:-kubexray-demo}"

# scenario | kubexray arguments | lines the output must match (extended regexps, ;-separated)
SCENARIOS=(
  "config|why deploy/checkout -n shop|^  diagnosis +The container checkout keeps exiting with code 1;^  likely cause +ConfigMap checkout-config was edited by kubectl patch"
  "bad-image|why deploy/api -n shop|^  diagnosis +Kubernetes can't pull nginx:1.72-alpine;^  very likely cause +image changed nginx:1.27-alpine → nginx:1.72-alpine"
  "oom|why deploy/reports -n shop|^  diagnosis +The container reports runs out of memory: Kubernetes kills it when it uses more than its 16Mi limit;^  very likely cause +resources changed .* by kubectl set"
  "bad-probe|why deploy/api -n shop|^  diagnosis +1 pod is running but failing its readiness probe;^  very likely cause +readiness probe added to api"
  "unschedulable|why deploy/checkout -n shop|^  diagnosis +The scheduler can't place 1 pod: 2 nodes have too little free memory;^  very likely cause +resources changed .* by kubectl set"
  "bad-config|why deploy/api -n shop|^  diagnosis +Kubernetes can't create the container api: couldn't find key password in Secret shop/db-credentials;^  very likely cause +Secret db-credentials was edited by kubectl patch"
  "node|incidents|^node $CLUSTER-worker2 is not ready.*: [0-9]+ apps? affected"
)

say() { printf '\033[1m%s\033[0m\n' "$*"; }

# matches reports whether every expected line is in the output.
matches() {
  local out="$1" expect
  IFS=';' read -ra expect <<<"$2"
  for e in "${expect[@]}"; do
    grep -Eq -- "$e" <<<"$out" || return 1
  done
}

run() {
  local name="$1" args="$2" want="$3" out="" waited=0
  say "== $name"
  "$ROOT/scripts/demo.sh" fix >/dev/null
  SCENARIO="$name" "$ROOT/scripts/demo.sh" break >/dev/null
  # Incidents appear after a 30 s grace period; give the cluster time too.
  while [ "$waited" -lt "$TIMEOUT" ]; do
    # shellcheck disable=SC2086 # args is a list of words
    out="$("$KX" $args --kubeconfig "$KUBECONFIG" 2>/dev/null || true)"
    if matches "$out" "$want"; then
      printf '%s\n' "$out" | grep -E '^  (diagnosis|very likely cause|likely cause)|apps? affected' || true
      return 0
    fi
    sleep 5
    waited=$((waited + 5))
  done
  echo "FAIL: $name: after ${TIMEOUT}s kubexray said:" >&2
  printf '%s\n' "$out" >&2
  echo "expected lines matching: $want" >&2
  kubectl -n shop get pods -o wide >&2 || true
  return 1
}

failed=()
for s in "${SCENARIOS[@]}"; do
  IFS='|' read -r name args want <<<"$s"
  if [ $# -gt 0 ] && [[ ! " $* " =~ \ $name\  ]]; then
    continue
  fi
  run "$name" "$args" "$want" || failed+=("$name")
done
"$ROOT/scripts/demo.sh" fix >/dev/null

if [ ${#failed[@]} -gt 0 ]; then
  echo "failed: ${failed[*]}" >&2
  exit 1
fi
say "Every scenario gave the expected diagnosis and cause."
