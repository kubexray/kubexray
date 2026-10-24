# 0011. Alerts: from Alertmanager to the likely cause

- Status: Accepted
- Date: 2026-10-24

## Context

Most teams already get paged by Prometheus Alertmanager. The alert says *that* something is wrong ("KubePodCrashLooping in shop"), and the next ten minutes go into finding *what changed*. kubexray, recording in the cluster (ADR-0010), already knows: the incident, the diagnosis, the likely cause and who made the change. The roadmap's v0.4 item is to put that next to the alert, in Slack or any webhook.

## Decision

### 1. kubexray is an Alertmanager receiver

`kubexray serve` accepts Alertmanager's webhook (version 4) at `POST /api/alertmanager`. Alertmanager authenticates with the same token as everything else, using its `http_config.authorization` with a `credentials_file`. Requests that carry a Bearer token skip the browser Host check: it protects cookies against DNS rebinding, and Alertmanager calls the in-cluster Service name.

Payloads are capped at 1 MiB and handled asynchronously, so Alertmanager gets `202 Accepted` at once.

### 2. Which app an alert is about

From the alert's labels, the way kube-state-metrics and the kube-prometheus rules name things: `namespace`, plus the first of `deployment`, `statefulset`, `daemonset`, `job_name` or `pod`. A pod is explained through its app, as `kubexray why` does. Alerts that name no app (node or cluster alerts) and apps kubexray doesn't know are skipped. kubexray adds context; it doesn't replace the alert.

### 3. What is sent

For each firing alert about a known app: the alert's name and summary, the app's state, the diagnosis, the likely cause with who made the change, and, with `--public-url`, a link that opens kubexray rewound to the moment the incident started with the app selected. Resolved alerts are not re-sent.

- **Slack**: an incoming webhook, one short message per alert. Cluster strings are escaped for Slack's markup.
- **Webhook**: the alert and the full incident as JSON, for anything else (PagerDuty event rules, a chat bot, a ticketing system).

Destinations are URLs from Secrets (`--slack-webhook-file`, `--webhook-url-file`); a webhook URL is a credential. kubexray makes no other outbound calls.

### 4. Noise

Alertmanager repeats notifications (`repeat_interval`). kubexray sends one message per alert fingerprint per 30 minutes, and bounds its queue, dropping the oldest when full, so a flood of alerts can't build up an unbounded backlog of outbound requests.

## Consequences

- The page arrives with "likely cause: image changed nginx:1.27 → 1.72 by helm, 2 minutes before" next to it, and a link straight to the moment.
- Nothing about Alertmanager's routing changes: kubexray is one more receiver, and whoever owns the routing decides what reaches it.
- Messages carry cluster details (names, images, who changed what) to Slack or the webhook. That's the point, and it's the operator's choice of destination; nothing is sent until one is configured.
