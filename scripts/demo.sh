#!/usr/bin/env bash
# The kubexray demo on a local kind cluster. It uses its own kubeconfig
# (.demo/kubeconfig) and never touches your usual kubectl configuration.
#
#   scripts/demo.sh up       create the cluster and deploy the shop app
#   scripts/demo.sh break    break it: SCENARIO=config (the default), bad-image, node,
#                            oom, bad-probe, unschedulable or bad-config
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
  for d in api checkout reports; do kubectl -n shop rollout status "deploy/$d" --timeout=180s >/dev/null; done
  kubectl -n shop rollout status sts/db --timeout=180s >/dev/null
  say "The shop is running. Starting kubexray…"
  echo "In another terminal, break it with: make break"
}

# live_pod prints one pod of an app that isn't already being deleted (right
# after a rollout, old pods linger while they terminate).
live_pod() {
  kubectl -n shop get pod -l "app=$1" -o go-template='{{range .items}}{{if not .metadata.deletionTimestamp}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}' | head -1
}

break_config() {
  say "1. Someone edits checkout-config: LOG_LEVEL=verbose (checkout only accepts debug, info, warn or error)."
  kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"verbose"}}' >/dev/null
  say "2. Nothing breaks: running pods keep the config they started with. Waiting ${DELAY}s…"
  sleep "$DELAY"
  say "3. A pod gets replaced, as during a node drain or an eviction."
  kubectl -n shop delete pod "$(live_pod checkout)" --wait=false >/dev/null
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
  # The worker running the most shop pods, so the failure hits several apps.
  local node
  node="$(kubectl -n shop get pods -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | grep -- '-worker' | sort | uniq -c | sort -rn | awk 'NR==1 {print $2}')"
  if [ -z "$node" ]; then
    echo "No shop pods run on a worker, so stopping one wouldn't affect the shop. Try: make fix && make break SCENARIO=node" >&2
    exit 1
  fi
  say "Worker $node fails (its container is stopped, like a machine losing power)."
  docker stop "$node" >/dev/null
  say "In about a minute Kubernetes marks the node NotReady and its pods not ready."
  say "kubexray groups everything it breaks into one incident. List it with:"
  echo "   ./bin/kubexray incidents --kubeconfig .demo/kubeconfig"
}

break_oom() {
  say "Someone trims reports' memory limit from 64Mi to 16Mi to save resources."
  kubectl -n shop set resources deploy/reports --requests=memory=16Mi --limits=memory=16Mi >/dev/null
  say "reports builds a 24 MB cache when it starts, so the new pod runs out of memory. Ask kubexray why:"
  echo "   ./bin/kubexray why deploy/reports -n shop --kubeconfig .demo/kubeconfig"
}

break_probe() {
  say "Someone adds a readiness probe to api on /healthz, a path nginx doesn't serve."
  kubectl -n shop patch deploy/api --type=json -p '[{"op":"add","path":"/spec/template/spec/containers/0/readinessProbe","value":{"httpGet":{"path":"/healthz","port":80},"periodSeconds":2}}]' >/dev/null
  say "The new pod runs but never becomes ready, so the rollout stalls. Ask kubexray why:"
  echo "   ./bin/kubexray why deploy/api -n shop --kubeconfig .demo/kubeconfig"
}

break_unschedulable() {
  say "Someone raises checkout's memory request to 64Gi, more than any node has."
  kubectl -n shop set resources deploy/checkout --requests=memory=64Gi --limits=memory=64Gi >/dev/null
  say "The new pod can't be scheduled. Ask kubexray why:"
  echo "   ./bin/kubexray why deploy/checkout -n shop --kubeconfig .demo/kubeconfig"
}

break_bad_config() {
  say "1. Someone renames the key in db-credentials from password to db-password. api reads password."
  kubectl -n shop patch secret db-credentials --type=json -p '[{"op":"move","from":"/data/password","path":"/data/db-password"}]' >/dev/null
  say "2. Running pods are fine. A pod gets replaced, as during a node drain."
  kubectl -n shop delete pod "$(live_pod api)" --wait=false >/dev/null
  say "The new pod can't start. Ask kubexray why:"
  echo "   ./bin/kubexray why deploy/api -n shop --kubeconfig .demo/kubeconfig"
}

fix() {
  for node in "$CLUSTER-worker" "$CLUSTER-worker2"; do
    if [ "$(docker inspect -f '{{.State.Running}}' "$node" 2>/dev/null)" = "false" ]; then
      say "Starting worker $node again…"
      docker start "$node" >/dev/null
      kubectl wait --for=condition=Ready "node/$node" --timeout=180s >/dev/null ||
        say "$node isn't ready yet; it will catch up. Continuing."
    fi
  done
  say "Restoring the shop…"
  kubectl -n shop patch configmap checkout-config --type=merge -p '{"data":{"LOG_LEVEL":"info"}}' >/dev/null
  if [ "$(kubectl -n shop get deploy api -o jsonpath='{.spec.template.spec.containers[0].image}')" != "nginx:1.27-alpine" ]; then
    kubectl -n shop set image deploy/api api=nginx:1.27-alpine >/dev/null
  fi
  # kubectl apply restores what the manifest sets (resources, the password
  # key); it doesn't remove what a patch added.
  if [ -n "$(kubectl -n shop get deploy api -o jsonpath='{.spec.template.spec.containers[0].readinessProbe}')" ]; then
    kubectl -n shop patch deploy/api --type=json -p '[{"op":"remove","path":"/spec/template/spec/containers/0/readinessProbe"}]' >/dev/null
  fi
  kubectl apply -f "$ROOT/demo/shop.yaml" >/dev/null
  if [ -n "$(kubectl -n shop get secret db-credentials -o jsonpath='{.data.db-password}')" ]; then
    kubectl -n shop patch secret db-credentials --type=json -p '[{"op":"remove","path":"/data/db-password"}]' >/dev/null
  fi
  kubectl -n shop rollout restart deploy/checkout >/dev/null
  for d in api checkout reports; do kubectl -n shop rollout status "deploy/$d" --timeout=180s >/dev/null; done
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
      oom) break_oom ;;
      bad-probe) break_probe ;;
      unschedulable) break_unschedulable ;;
      bad-config) break_bad_config ;;
      *) echo "unknown SCENARIO=$SCENARIO (config, bad-image, node, oom, bad-probe, unschedulable or bad-config)" >&2; exit 1 ;;
    esac ;;
  fix) fix ;;
  down) down ;;
  *) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
