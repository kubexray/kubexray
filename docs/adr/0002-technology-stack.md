# 0002. Technology stack

- Status: Accepted
- Date: 2026-10-20

## Context

kubexray needs to watch cluster state efficiently, push frequent small updates to a browser, and render an animated graph of potentially thousands of objects. It should ship as a single artifact that runs both locally and in-cluster.

## Decision

- **Backend: Go** with `client-go` shared informers. It is the native ecosystem for Kubernetes tooling, informers give us efficient watch-and-cache semantics, and Go produces a single static binary.
- **Transport: WebSocket** carrying a full snapshot on connect, then incremental deltas. The delta format is versioned from day one, because it is reused for rewind and replay files.
- **UI: React + TypeScript (strict) + Cytoscape.js**, built with Vite. Cytoscape.js provides graph layouts, compound nodes (namespaces, nodes) and animation; a WebGL renderer can be added later for very large clusters.
- **Packaging:** UI embedded into the Go binary with `embed`; distroless non-root container image; Helm chart for in-cluster installs.
- **Tooling:** golangci-lint, govulncheck, gitleaks and actionlint pinned in the Makefile and run via `go run`, so contributors need only Go and Docker.

## Consequences

- One language per tier keeps the codebase approachable.
- Embedding the UI couples UI and backend releases, which is acceptable since they share the delta schema.
- Cytoscape.js performance must be measured early (performance baseline in v0.1); if it falls short at scale we revisit with an ADR.
