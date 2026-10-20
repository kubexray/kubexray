# 0001. Record architecture decisions

- Status: Accepted
- Date: 2026-10-20

## Context

kubexray will make a number of decisions (data model, transport, security controls) that are expensive to revisit and easy to forget the reasoning behind.

## Decision

We record significant decisions as ADRs in `docs/adr/`, using the template in the README. A decision is "significant" if it affects the public interface, the security model, the delta/replay schema, or would take more than a day to reverse.

## Consequences

Reasoning is preserved for future contributors. Writing an ADR adds a small upfront cost to architectural changes, which is intended.
