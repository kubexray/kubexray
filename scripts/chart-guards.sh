#!/usr/bin/env bash
# The chart refuses values that would widen kubexray's read-only access.
set -euo pipefail

chart="${1:-charts/kubexray}"
fail() { echo "chart-guards: $*" >&2; exit 1; }

# Each of these would grant reading Secrets: by name, by a group-less path,
# or through wildcards.
for r in v1/secrets /v1/secrets example.com/v1/secrets '*/v1/*' 'v1/*' 'apps/*/deployments'; do
  if helm template guard "$chart" --set "diffs.extraResources[0]=$r" >/dev/null 2>&1; then
    fail "diffs.extraResources=$r rendered; it must be refused"
  fi
done

out=$(helm template guard "$chart" --set 'diffs.extraResources[0]=argoproj.io/v1alpha1/applications')
grep -q 'resources: \["applications"\]' <<<"$out" || fail "a named custom resource wasn't granted"
if grep -q '"\*"' <<<"$out"; then
  fail "the rendered chart grants a wildcard"
fi
echo "chart-guards: ok"
