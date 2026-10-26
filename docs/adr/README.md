# Architecture Decision Records

Short documents capturing significant decisions, their context and consequences. One file per decision, numbered, never rewritten. A superseded ADR is marked as such and links to its replacement.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-technology-stack.md) | Technology stack | Accepted |
| [0003](0003-graph-delta-schema.md) | Graph delta schema | Accepted |
| [0004](0004-node-ids-from-names.md) | Node IDs from kind, namespace and name | Accepted |
| [0005](0005-change-facts-and-attribution.md) | Change facts and attribution | Accepted |
| [0006](0006-likely-cause-engine.md) | Likely-cause engine | Accepted |
| [0007](0007-local-web-ui.md) | Local web UI: server, security and build | Accepted |
| [0008](0008-replays-and-sharing.md) | Replays, sharing and grouped incidents | Accepted |
| [0009](0009-diagnosis.md) | Diagnosis: explaining the symptom itself | Accepted |
| [0010](0010-in-cluster-mode.md) | In-cluster mode: always-on recording | Accepted |
| [0011](0011-alerts.md) | Alerts: from Alertmanager to the likely cause | Accepted |
| [0012](0012-change-feed.md) | Change feed: every change, what it did | Accepted |
| [0013](0013-sign-in.md) | Sign-in for kubexray on the internet | Accepted |
| [0014](0014-ai-second-opinion.md) | A second opinion from the logs, by a language model | Proposed |

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD

## Context
What problem are we solving, what forces are at play?

## Decision
What we decided.

## Consequences
What becomes easier, what becomes harder, what we must watch.
```
