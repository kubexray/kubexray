#!/usr/bin/env bash
# The Helm chart end to end (ADR-0010): installs kubexray in the demo kind
# cluster from a locally built image and checks authentication, least
# privilege, and that history survives a pod restart.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KUBECONFIG="$ROOT/.demo/kubeconfig"
CLUSTER="${DEMO_CLUSTER:-kubexray-demo}"
NS=kubexray
RELEASE=kx
SVC="$RELEASE-kubexray"
PORT="${E2E_PORT:-18080}"
IMAGE=kubexray:e2e

say() { printf '\033[1m%s\033[0m\n' "$*"; }
fail() { echo "FAIL: $*" >&2; kubectl -n "$NS" get pods -o wide >&2 || true; kubectl -n "$NS" logs "deploy/$SVC" --tail=50 >&2 || true; exit 1; }

PF=""
forward() {
  [ -n "$PF" ] && kill "$PF" 2>/dev/null || true
  kubectl -n "$NS" port-forward "svc/$SVC" "$PORT:80" >/dev/null 2>&1 &
  PF=$!
  for _ in $(seq 1 50); do
    curl -fsS "http://localhost:$PORT/healthz" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  fail "port-forward to $SVC never answered"
}
trap '[ -n "$PF" ] && kill "$PF" 2>/dev/null || true' EXIT

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

say "Building $IMAGE and loading it into ${CLUSTER}…"
docker build -q -t "$IMAGE" "$ROOT" >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null

say "Installing the chart…"
helm upgrade --install "$RELEASE" "$ROOT/charts/kubexray" -n "$NS" --create-namespace \
  --set image.repository=kubexray --set image.tag=e2e --set image.pullPolicy=Never \
  --set persistence.size=256Mi --set retentionSizeMB=128 --wait --timeout 3m >/dev/null
TOKEN="$(kubectl -n "$NS" get secret "$SVC" -o jsonpath='{.data.token}' | base64 -d)"
[ "${#TOKEN}" -ge 32 ] || fail "the generated token is too short"
forward

say "Authentication…"
[ "$(code "http://localhost:$PORT/api/history")" = 401 ] || fail "the API answered without a token"
[ "$(code -H "Authorization: Bearer wrong-$TOKEN" "http://localhost:$PORT/api/history")" = 401 ] || fail "the API accepted a wrong token"
[ "$(code -H "Authorization: Bearer $TOKEN" "http://localhost:$PORT/api/history")" = 200 ] || fail "the API refused the token"
[ "$(code -H "Host: evil.example" "http://localhost:$PORT/?token=$TOKEN")" = 421 ] || fail "an unknown Host was accepted"

say "Least privilege…"
SA="system:serviceaccount:$NS:$SVC"
[ "$(kubectl auth can-i list pods --as="$SA")" = yes ] || fail "can't list pods"
[ "$(kubectl auth can-i list secrets --as="$SA" || true)" = no ] || fail "can list Secrets with rbac.secrets off"
[ "$(kubectl auth can-i delete pods --as="$SA" || true)" = no ] || fail "can delete pods"

say "History survives a restart…"
history() { curl -fsS -H "Authorization: Bearer $TOKEN" "http://localhost:$PORT/api/history"; }
FROM="$(history | sed -E 's/.*"from":"([^"]+)".*/\1/')"
[ -n "$FROM" ] || fail "no history recorded"
kubectl -n shop rollout restart deploy/checkout >/dev/null # some changes to record
sleep 5
kubectl -n "$NS" delete pod -l app.kubernetes.io/instance="$RELEASE" --wait=true >/dev/null
kubectl -n "$NS" rollout status "deploy/$SVC" --timeout=120s >/dev/null
forward
AFTER="$(history | sed -E 's/.*"from":"([^"]+)".*/\1/')"
[ "$AFTER" = "$FROM" ] || fail "history starts at $AFTER after the restart, was $FROM"
[ "$(code -H "Authorization: Bearer $TOKEN" "http://localhost:$PORT/api/at?t=$FROM")" = 200 ] || fail "can't rewind to before the restart"

say "Upgrades keep the token…"
helm upgrade "$RELEASE" "$ROOT/charts/kubexray" -n "$NS" --reuse-values --wait --timeout 3m >/dev/null
[ "$(kubectl -n "$NS" get secret "$SVC" -o jsonpath='{.data.token}' | base64 -d)" = "$TOKEN" ] || fail "the token changed on upgrade"

helm uninstall "$RELEASE" -n "$NS" >/dev/null
say "The chart installs, authenticates, keeps to read-only access and keeps history across restarts."
