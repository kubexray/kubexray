#!/usr/bin/env bash
# The kubexray demo on a local kind cluster. It uses its own kubeconfig
# (.demo/kubeconfig) and never touches your usual kubectl configuration.
#
#   scripts/demo.sh up       create the cluster and deploy the shop app
#   scripts/demo.sh break    break it (SCENARIO=config, the default, bad-image or node)
#   scripts/demo.sh fix      undo every scenario
#   scripts/demo.sh down     delete the cluster
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${DEMO_CLUSTER:-kubexray-demo}"
export KUBECONFIG="$ROOT/.demo/kubeconfig"
SCENARIO="${SCENARIO:-config}"
DELAY="${BREAK_DELAY:-45}"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
need() { command -v "$1" >/dev/null || { echo "demo needs $1: $2" >&2; exit 1; }; }

up() {
  need docker "https://docs.docker.com/get-docker/"
  need kind "brew install kind, or https://kind.sigs.k8s.io"
  need kubectl "brew install kubectl"
  mkdir -p "$ROOT/.demo"
  if kind get clusters 2>/dev/null | grep -qx "$CLUSTER" && [ "$(kind get nodes --name "$CLUSTER" | wc -l | tr -d ' ')" -lt 3 ]; then
    say "Recreating $CLUSTER with two workers…"
    kind delete cluster --name "$CLUSTER" >/dev/null
  fi
  if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    say "Creating kind cluster $CLUSTER (about a minute)…"
    kind create cluster --name "$CLUSTER" --config "$ROOT/demo/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 180s
  else
    kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null
  fi
  say "Deploying the shop app…"
  kubectl apply -f "$ROOT/demo/shop.yaml" >/dev/null
  for d in api checkout; do kubectl -n shop rollout status "deploy/$d" --timeout=180s >/dev/null; done
  kubectl -n shop rollout status sts/db --timeout=180s >/dev/null
  say "The shop is running. Starting kubexray…"
  echo "In another terminal, break it with: make break"
}

break_config() {
  say "1. Someone edits checkout-config: LOG_LEVEL=verbose (checkout only accepts debug, info, warn or error)."
  kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"verbose"}}' >/dev/null
  say "2. Nothing breaks: running pods keep the config they started with. Waiting ${DELAY}s…"
  sleep "$DELAY"
  say "3. A pod gets replaced, as during a node drain or an eviction."
  kubectl -n shop delete "$(kubectl -n shop get pod -l app=checkout -o name | head -1)" --wait=false >/dev/null
  say "The new pod reads the bad value and crash-loops. Ask kubexray why:"
  echo "   ./bin/kubexray why deploy/checkout -n shop --kubeconfig .demo/kubeconfig"
}

break_image() {
  say "Someone ships api with an image tag that doesn't exist (nginx:1.72-alpine)."
  kubectl -n shop set image deploy/api api=nginx:1.72-alpine >/dev/null
  say "New pods can't pull it. Ask kubexray why:"
  echo "   ./bin/kubexray why deploy/api -n shop --kubeconfig .demo/kubeconfig"
}

break_node() {
  local node="$CLUSTER-worker2"
  say "Worker $node fails (its container is stopped, like a machine losing power)."
  docker stop "$node" >/dev/null
  say "In about a minute Kubernetes marks the node NotReady and its pods not ready."
  say "kubexray groups everything it breaks into one incident. List it with:"
  echo "   ./bin/kubexray incidents --kubeconfig .demo/kubeconfig"
}

fix() {
  if [ "$(docker inspect -f '{{.State.Running}}' "$CLUSTER-worker2" 2>/dev/null)" = "false" ]; then
    say "Starting worker $CLUSTER-worker2 again…"
    docker start "$CLUSTER-worker2" >/dev/null
    kubectl wait --for=condition=Ready "node/$CLUSTER-worker2" --timeout=180s >/dev/null
  fi
  say "Restoring checkout-config and api…"
  kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"info"}}' >/dev/null
  kubectl -n shop rollout restart deploy/checkout >/dev/null
  if [ "$(kubectl -n shop get deploy api -o jsonpath='{.spec.template.spec.containers[0].image}')" != "nginx:1.27-alpine" ]; then
    kubectl -n shop set image deploy/api api=nginx:1.27-alpine >/dev/null
  fi
  for d in api checkout; do kubectl -n shop rollout status "deploy/$d" --timeout=180s >/dev/null; done
  say "Fixed."
}

down() {
  kind delete cluster --name "$CLUSTER"
  rm -f "$KUBECONFIG"
}

case "${1:-}" in
  up) up ;;
  break)
    case "$SCENARIO" in
      config) break_config ;;
      bad-image) break_image ;;
      node) break_node ;;
      *) echo "unknown SCENARIO=$SCENARIO (config, bad-image or node)" >&2; exit 1 ;;
    esac ;;
  fix) fix ;;
  down) down ;;
  *) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
