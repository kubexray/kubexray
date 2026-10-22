# Architecture Decision Records

Short documents capturing significant decisions, their context and consequences. One file per decision, numbered, never rewritten. A superseded ADR is marked as such and links to its replacement.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-technology-stack.md) | Technology stack | Accepted |
| [0003](0003-graph-delta-schema.md) | Graph delta schema | Accepted |
| [0004](0004-node-ids-from-names.md) | Node IDs from kind, namespace and name | Accepted |
| [0005](0005-change-facts-and-attribution.md) | Change facts and attribution | Accepted |

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
