# 0007. Local web UI: server, security and build

- Status: Proposed
- Date: 2026-10-23

## Context

v0.1 ships the approved design ([prototype](https://claude.ai/artifact/XyqRw1wvcfxxETgFPK9P37)) as a web UI served by the kubexray binary: the live map, incident cards from the cause engine (ADR-0006), and rewind from the recorder.

The UI runs on a developer's laptop while kubexray holds credentials that can read a whole cluster, often production. A local web server is the most exposed part of the product: any web page the user visits can try to talk to `127.0.0.1` (the [threat model](../threat-model.md), boundary A). This ADR fixes how the server is protected, what it exposes, and how the UI is built and shipped.

## Decision

### 1. Starting the UI

`kubexray` with no command starts the UI (`kubexray help` prints usage). It syncs the cluster, starts the recorder, starts the server and opens the browser:

```text
$ kubexray
Reading cluster "prod-eu" at https://… (read-only)…
kubexray is running at http://127.0.0.1:52817/?token=…   (Ctrl-C to stop)
```

Flags: `--port` (default: a random free port), `--no-browser`, plus the existing `--context`, `--kubeconfig`, `--history` and `--history-max-mb`.

### 2. Who can talk to the server

| Threat | Control |
|---|---|
| Other machines on the network | Listen on `127.0.0.1` only. There is no flag to listen elsewhere in v0.1; in-cluster access comes with its own authentication in v0.4. |
| A malicious web page making requests to the port (CSRF, cross-site WebSocket hijacking) | **Session token:** 256 random bits, generated at start, printed only in the launch URL. Opening the URL sets an `HttpOnly`, `SameSite=Strict` cookie and redirects to `/`, which removes the token from the address bar and history. Every API call and the WebSocket upgrade require the cookie; tokens are compared in constant time. **WebSocket upgrades also require** `Origin` to be exactly the server's own origin. |
| DNS rebinding (a hostile domain resolving to 127.0.0.1) | Reject any request whose `Host` isn't `127.0.0.1:<port>` or `localhost:<port>`. |
| Script injection via cluster data (names, labels, images) | The UI renders cluster strings as text only (React escaping; `dangerouslySetInnerHTML` banned by lint) and a strict **Content-Security-Policy**: `default-src 'self'`, no inline scripts, `connect-src` limited to the server, `frame-ancestors 'none'`. |
| Data leaving the machine | The UI makes **no external requests**: fonts and all code are bundled, nothing is loaded from a CDN, no analytics. |
| Misbehaving clients | At most 16 WebSocket clients; incoming messages capped at 4 KiB; each client has a bounded send queue and is disconnected if it falls behind (it reconnects and resyncs from a snapshot); keepalive pings and write timeouts. |

Plus standard headers on every response: `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store` for the API.

### 3. What the server exposes

| Endpoint | Purpose |
|---|---|
| `GET /api/stream` (WebSocket) | The live stream from the pump (ADR-0003): a snapshot, then deltas. An `incidents` message carries the cause engine's current incidents whenever they change (checked once a second). |
| `GET /api/at?t=<RFC3339>` | The cluster at a past moment, rebuilt by the recorder (a snapshot message), plus the incidents at that moment. Used by the timeline. |
| `GET /api/history` | The recorded time range. |
| `GET /api/why?id=<node id>` | The cause engine's explanation for one app, for the side panel. |

All endpoints are read-only. The server has no write access to the cluster.

**Fan-out:** the pump stays the graph's only flusher (ADR-0003, recorder); a hub receives its messages and copies them to each client. A new client gets a fresh snapshot and then every delta after it. Deltas at or below the snapshot's `seq` are ignored by the client, which keeps join-time races harmless.

### 4. The UI build

- `web/`: **Vite + React + TypeScript (strict)**, with no UI component library. The design tokens and layout come from the approved prototype.
- **Fonts are self-hosted** (Bricolage Grotesque and IBM Plex, both under the SIL Open Font License) via `@fontsource` packages, so the UI makes no requests to Google.
- **Types are generated from Go** with `tygo` (ADR-0003) into `web/src/gen/`; CI fails if they're stale.
- **Embedding:** `vite build` writes to `internal/web/dist`, which the Go binary embeds with `embed`. A small placeholder page is committed there so `go build` works without Node; `make build` and release builds run the real UI build first.
- **npm** with a lockfile (`npm ci`), Node version pinned in `.nvmrc`; Dependabot also watches npm.
- **Tests:** Vitest for the client state logic (applying snapshots and deltas, rewind). Playwright end-to-end tests come in v0.4 with the CI work.
- **Lint:** ESLint with `react/no-danger` as an error.

### 5. CI changes

- A new `web` job: `npm ci`, type check, lint, test, build.
- CodeQL adds `javascript-typescript`.
- The `main` ruleset's required checks must be updated to include the new jobs, or PRs would merge without them.

## Alternatives considered

| Option | Why not |
|---|---|
| Token in the URL on every request, or in local storage | Leaks through history, logs and referrers; cookies set by the server are invisible to page scripts with `HttpOnly` |
| Server-Sent Events instead of WebSocket | Simpler, but one-way; the timeline and future actions need requests the other way, which would need a second channel |
| Svelte or plain TypeScript instead of React | Smaller bundles, but React is what most contributors and reviewers know, and the UI is small enough that size doesn't matter |
| Committing the built UI to git | Keeps `go install` self-contained, but puts generated files in every diff and review; release binaries and `make build` cover installs |
| Google Fonts from the CDN | Each page load would tell Google that someone opened kubexray; unacceptable for a tool used on production clusters |

## Consequences

- Running `kubexray` is safe on a laptop that also browses the web: a hostile page can't read the stream or the API without the token, and can't reach the server through a hostile domain.
- Contributors need Node for UI work. Go-only changes still build and test without it.
- Two new required checks; the ruleset update is a one-time admin step.
- `kubexray` with no arguments now starts the UI instead of printing usage.
