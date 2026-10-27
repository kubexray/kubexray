# 0015. Diffs: what changed, without the values

- Status: Accepted
- Date: 2026-10-27

## Context

The change feed (ADR-0012) says a change was made and what it did. For many changes it can't say what the change was: kubexray recorded a pod template's images, resources and whether probes exist (ADR-0005), and nothing else. A hand edit that adds an environment variable reads "pod template changed (revision 2 → 3)", which sends people to `kubectl rollout history` to find out what happened. People expect to see a diff.

A full diff of a pod template would carry environment variable values, commands and arguments. Those regularly hold credentials, and kubexray's recordings and replays are meant to be shared. ADR-0005 kept them out for that reason.

## Decision

### 1. What is recorded

kubexray records these parts of a pod template:

- **Environment variables:** each name, and where its value comes from: `ConfigMap/checkout-config:LOG_LEVEL`, `Secret/db:password`, `field:metadata.name`, `resource:limits.memory`, or a literal. A literal's value is kept only as a fingerprint (section 2).
- **envFrom:** the ConfigMaps and Secrets it reads.
- **Volumes:** the ConfigMaps, Secrets and PersistentVolumeClaims they come from.
- **The service account, and ports.**
- **What each probe checks:** `http GET :8080/healthz`, `tcp :5432`, `grpc :9000`. An exec probe is recorded as "a command", with its contents left out.
- **The command and arguments:** a fingerprint only.

Values are still never recorded: not literal environment values, not commands, not arguments. Neither are ConfigMap or Secret contents, which kubexray doesn't read.

### 2. Fingerprints

A fingerprint is an HMAC-SHA256 of the value, truncated to 12 hex characters. Its key is random for each kubexray run and is never written anywhere. Two fingerprints from the same run show whether a value changed, and nothing else. A fingerprint in a recording or a shared replay can't be matched against guesses, such as `true` or a common password, because the key is gone. A restart is already a new baseline for the feed (ADR-0012), so fingerprints from different runs are never compared.

### 3. Diffs in the feed

Each change to a workload carries diff lines: what was added, removed or changed, with before and after where kubexray has them. It covers:

- the image, resources and probes
- each environment variable, envFrom, volumes, ports and the service account
- the command
- replicas, and the release version

Where only a fingerprint changed, the line says so: "env LOG_LEVEL changed · values aren't recorded". A pod template change with nothing more specific to say is described from its diff ("env PROMO_BANNER added (revision 2 → 3)"), not as "pod template changed".

The UI shows the lines under **See diff**, git style: `-` before, `+` after, `~` changed but not recorded. A ConfigMap or Secret edit shows that its contents aren't read, so there are no values to compare.

### 4. Anonymized replays

The anonymizer renames everything new that can name things:

- environment variable names
- the objects and keys values come from
- envFrom and volume references
- the service account
- probe paths, which become `http GET :8080/…`

Fingerprints and ports stay, because they say nothing by themselves. The feed in a replay is built from the anonymized messages, so its diffs are anonymized too.

## Consequences

- "What changed?" has an answer in kubexray itself, down to the variable, port or probe.
- Recordings grow a little: a few names per container.
- Environment variable names are now in recordings and in replays that aren't anonymized. Names rarely hold secrets. Teams that worry about that share anonymized replays.
- ConfigMap edits still can't show what changed, because kubexray doesn't read contents. Showing them would need opt-in reading, which is a separate decision.
