#!/usr/bin/env bash
# A half hour in the life of the demo shop, for recording kubexray's public
# demo replay: scaling, deploys, Helm upgrades, a hand edit (drift), and two
# incidents with their fixes. Runs on the demo cluster (make demo) with its
# own kubeconfig; it never touches your usual kubectl configuration.
#
#   scripts/demo-story.sh prepare      put the shop back as it starts, and
#                                      load the story's images into the nodes
#   scripts/demo-story.sh              about 30 minutes
#   STORY_MINUTE=5 scripts/demo-story.sh   faster: 5 seconds per minute
#
# Prepare first, then start kubexray so it records from the beginning, then
# run the story and export the whole recording as a replay.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export KUBECONFIG="$ROOT/.demo/kubeconfig"
CLUSTER="${DEMO_CLUSTER:-kubexray-demo}"
MINUTE="${STORY_MINUTE:-60}" # seconds in one story minute

if [ "${1:-}" = "prepare" ]; then
  # Images already on the nodes: rollouts in the recording take seconds,
  # not however long a pull takes on a fresh node.
  for node in $(kind get nodes --name "$CLUSTER"); do
    for image in nginx:1.27-alpine nginx:1.28-alpine busybox:1.36 busybox:1.37; do
      docker exec "$node" crictl pull "$image" >/dev/null
    done
  done
  helm uninstall storefront -n shop --wait >/dev/null 2>&1 || true
  kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"info"}}' >/dev/null
  kubectl -n shop set image deploy/api api=nginx:1.27-alpine >/dev/null
  kubectl -n shop scale deploy/api --replicas=3 >/dev/null
  kubectl -n shop set image deploy/reports reports=busybox:1.36 >/dev/null
  kubectl -n shop scale deploy/reports --replicas=1 >/dev/null
  for d in api checkout reports; do kubectl -n shop rollout status "deploy/$d" --timeout=3m >/dev/null; done
  echo "The shop is ready for the story."
  exit 0
fi
START=$(date +%s)

say() { printf '\033[1m[%5s] %s\033[0m\n' "$(elapsed)" "$*"; }
elapsed() { local s=$(($(date +%s) - START)); printf '%d:%02d' $((s / 60)) $((s % 60)); }

# at waits until a story minute (decimals allowed), then says what happens.
at() {
  local target
  target=$(awk -v m="$1" -v s="$MINUTE" -v t0="$START" 'BEGIN { printf "%d", t0 + m * s }')
  local now
  now=$(date +%s)
  if [ "$target" -gt "$now" ]; then sleep $((target - now)); fi
  shift
  say "$*"
}

helm_storefront() { # release version, image, replicas
  helm upgrade --install storefront "$ROOT/demo/storefront" -n shop \
    --set release="$1" --set image="$2" --set replicas="$3" --wait --timeout 3m >/dev/null
}

checkout_pod() {
  kubectl -n shop get pods -l app=checkout --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}'
}

at 0.5 "Traffic picks up: api scaled 3 → 4"
kubectl -n shop scale deploy/api --replicas=4 >/dev/null

at 2 "The storefront is installed with Helm (1.0.0)"
helm_storefront 1.0.0 nginx:1.27-alpine 2

at 4 "reports gets a new image: busybox 1.36 → 1.37"
kubectl -n shop set image deploy/reports reports=busybox:1.37 >/dev/null

at 6 "Helm upgrade storefront 1.0.0 → 1.1.0 (nginx 1.28)"
helm_storefront 1.1.0 nginx:1.28-alpine 2

at 8 "Someone hand-edits the Helm-managed storefront (drift)"
kubectl -n shop set env deploy/storefront PROMO_BANNER=on >/dev/null

at 10 "checkout-config is edited: LOG_LEVEL=verbose (checkout accepts debug, info, warn, error)"
kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"verbose"}}' >/dev/null

at 11 "A checkout pod is replaced; the new one reads the bad value and crash-loops"
kubectl -n shop delete pod "$(checkout_pod)" --wait=false >/dev/null

at 13.5 "The edit is put back: LOG_LEVEL=info"
kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"info"}}' >/dev/null

at 15 "Traffic drops: api scaled 4 → 2"
kubectl -n shop scale deploy/api --replicas=2 >/dev/null

at 17 "api ships an image tag that doesn't exist: nginx:1.72-alpine"
kubectl -n shop set image deploy/api api=nginx:1.72-alpine >/dev/null

at 19.5 "The rollout is undone"
kubectl -n shop rollout undo deploy/api >/dev/null

at 21 "Helm upgrade storefront 1.1.0 → 1.2.0 (puts the hand edit back to what Helm says)"
helm_storefront 1.2.0 nginx:1.28-alpine 2

at 23 "reports scaled 1 → 2 for the month-end run"
kubectl -n shop scale deploy/reports --replicas=2 >/dev/null

at 25 "Traffic is back: api scaled 2 → 3"
kubectl -n shop scale deploy/api --replicas=3 >/dev/null

at 27 "Helm upgrade storefront 1.2.0 → 1.2.1, three replicas"
helm_storefront 1.2.1 nginx:1.28-alpine 3

at 30 "Done: everything settles. Export the replay now."
