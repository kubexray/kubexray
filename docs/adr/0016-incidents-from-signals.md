# 0016. Incidents from signals: errors, latency, restarts and saturation

- Status: Accepted
- Date: 2026-10-27

## Context

kubexray opens an incident when Kubernetes says an app is broken (ADR-0006): pods not ready, crash-looping, unable to pull or schedule, refused by a quota, or an app scaled to zero with traffic routed to it. That catches what crashes. It misses what users feel while every pod looks healthy:

- a deploy that makes 8% of requests fail
- an app that got ten times slower
- a pod that restarts every twenty minutes
- memory creeping toward its limit before a nightly crash

These show up in metrics. Prometheus alerts can catch them, but only for the alerts someone wrote, and an alert doesn't know what changed just before. kubexray does.

Clusters expose these numbers in different places. Ingress controllers (Traefik, ingress-nginx), Gateway API implementations built on Envoy, service meshes and the apps themselves (OpenTelemetry) publish to Prometheus. An AWS Application Load Balancer publishes to CloudWatch. Memory and CPU come from metrics-server. Many clusters have some of these, few have all.

## Decision

### 1. Four signals per app

| Signal | Breaks when | Sources |
|---|---|---|
| Errors | the share of requests answered with 5xx | Traefik, ingress-nginx, Envoy (Gateway API, Istio), Linkerd, OpenTelemetry HTTP server metrics, ALB via CloudWatch |
| Latency | p99 response time | the same |
| Restarts | container restarts per hour | Kubernetes, always |
| Saturation | memory against the limit, CPU throttling | metrics-server, or cAdvisor in Prometheus |

### 2. Finding sources, without setup

On start, and every few minutes after:

- **Prometheus** is found by its Service (name `prometheus`, or the label `app.kubernetes.io/name=prometheus`, port 9090), or set with `prometheus.url`. Hosted Prometheus takes a token from a Secret.
- **Which exporters exist** is found by asking Prometheus which metric names it has. Each source is a small adapter: the queries for error rate and p99, and how its labels name a Service. For example, Traefik's `namespace-service-port@kubernetes`, or ingress-nginx's `service`.
- **AWS ALB** metrics are found through the `TargetGroupBinding` objects the AWS Load Balancer Controller creates. They link a target group to a Service. CloudWatch is read only when `aws.cloudwatch` is on, with a read-only role from EKS Pod Identity or IRSA, and only once a minute, because CloudWatch charges per request.
- **metrics-server** is read through the Kubernetes API (`metrics.k8s.io`).

kubexray only reads. Locally it reaches Prometheus through the Kubernetes API's service proxy with the user's kubeconfig. In the cluster it calls the Service directly: kubexray is never given `services/proxy`, which would let it reach any Service. Its chart's NetworkPolicy only limits what comes in. If Prometheus has a NetworkPolicy, it has to let kubexray's pods in.

The first adapters are Traefik (`traefik_service_requests_total`, whose `namespace-name-port@kubernetes` label is matched against known Services), ingress-nginx, Istio (`istio_requests_total` reported by the destination) and OpenTelemetry HTTP server metrics, when the collector promotes the Kubernetes namespace to a label. An app behind several is read from the first. Every minute kubexray asks each for requests, 5xx responses and p99 latency per Service over the last 2 minutes, and maps the Service to the app whose pods it selects.

### 3. Numbers to apps

Every source names a Service, a target group or a pod. kubexray's map already knows which app each one belongs to (Service → Deployment, TargetGroupBinding → Service, pod → owner), so a signal attaches to an app. Signals without a mapping are dropped and counted, and the sources page says so.

### 4. Normal, and a break

For each app, kubexray keeps a week of its error rate and p99 at 1-minute resolution. Minutes that were part of a break are marked and never count as normal, so an outage doesn't teach kubexray that failing is normal. On start it relearns the history from its own recording. Each app is judged only against itself:

- **Normal** is the median and the typical spread (the median absolute deviation, scaled like a standard deviation, and never less than 5% of the median). It comes from the same hour on other days once that hour has three days of history, and from the last 24 hours until then.
- A **break** is a value above the median by more than K spreads, where K is 6 on Calm, 4 on Normal and 3 on Strict. It must also be above floors that hold for every app: 1% errors; for latency 200 ms, and 1.5 times the usual p99. And the app must have at least 0.2 requests a second.
- It must last the **hold**: 5 minutes on Calm, 2 on Normal, 1 on Strict (`--sensitivity`, `signals.sensitivity`). The incident starts when the break began, not when it was confirmed.

Cards show the normal range as the middle 90% of the app's minutes: "8.1% of requests fail (12 a second, normal 0.1%–0.6%)".

Restarts break on a rate (3 or more an hour), not on a baseline. Memory breaks at 90% of the limit held for 5 minutes; once baselines exist, a rising trend that reaches the limit within the hour will too.

Kubernetes keeps only a restart count and the latest restart's time, so the watcher notes when each restart happens and keeps the last ten per pod. A pod first seen after restarting has one known time. Memory comes from metrics-server every 30 seconds, as a pod's highest container use against its limit, in steps of 5%, so the recording only grows when the step changes. Containers without a memory limit don't count.

With less than an hour of history there's no baseline, and fixed thresholds apply: 5% errors or a 1-second p99. A fresh install neither guesses nor fires on noise.

A baseline can't tell a long-standing problem from normal. An app that has always failed 4% of requests is flagged when it gets worse, not for the 4%. kubexray reports what changed.

### 5. An incident like any other

Errors and latency break against each app's baseline (section 4). The numbers are recorded per app on a small node of kind Traffic, once a minute, so rewinds and replays show them.

A signal incident goes through the cause engine (ADR-0006) like a pod incident. When requests fail or slow down, changes to what routes them also count: the Services that select the app's pods, the Ingresses that route to those Services, and NetworkPolicies that select the pods (by `matchLabels`). A routing change made shortly before errors began is the likely cause, and the card shows its diff, for example `spec.ports[0].targetPort 80 → 9998`. An HPA that targets the app counts for any signal. That needs values on, since what it targets is a value. the changes to the app and what it uses in the 30 minutes before, with evidence. "Errors started 40 seconds after the new pods became ready" is the signal's version of "only pods started after the change fail". Every incident says what detected it (source and numbers), and its card shows the signal's chart with the changes marked. An app whose pods are failing and whose errors are up is one incident with two signals.

Firing Alertmanager alerts (ADR-0011) also become incidents on the map, tagged as the user's own alert.

### 6. Recorded with everything else

Signal values are recorded at 1-minute resolution in the recording (one small message per minute), so rewind, replays and post-mortems show them. They carry no names beyond what the map already has. Anonymized replays rename them like every other name.

## Consequences

- kubexray notices what users feel, not only what crashes, and still names the change that likely caused it.
- Without Prometheus, it gets restarts and saturation, not errors or latency. The sources page says what's missing and how to add it.
- Baselines can be wrong, especially just after install and for spiky apps. Floors, sustained times, the sensitivity setting and showing the numbers on every incident keep this visible and adjustable.
- Each new source is an adapter with fixtures from its real metrics. Traefik, ingress-nginx, Envoy, OpenTelemetry and ALB come first, then Linkerd, Kong and HAProxy.
