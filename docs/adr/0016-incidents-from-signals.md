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

kubexray only reads. Locally it reaches Prometheus through the Kubernetes API's service proxy with the user's kubeconfig. In the cluster it calls the Service directly, and the chart's NetworkPolicy allows it.

### 3. Numbers to apps

Every source names a Service, a target group or a pod. kubexray's map already knows which app each one belongs to (Service → Deployment, TargetGroupBinding → Service, pod → owner), so a signal attaches to an app. Signals without a mapping are dropped and counted, and the sources page says so.

### 4. Normal, and a break

For each app and signal, kubexray keeps a baseline from the last 24 hours at 1-minute resolution: the usual range, built from robust statistics (median and spread). The range for a given time of day comes from the same hour in the window when there's enough history, and from the whole window otherwise.

A **break** is when a signal is clearly outside its range, and also above an absolute floor (no incident for 0.01% → 0.05% errors, or for an app with almost no traffic), for a sustained time. The sustained time comes from the sensitivity setting: Calm 5 minutes, Normal 2, Strict 1. The incident starts when the break began, not when it was confirmed.

Restarts break on a rate (3 or more an hour), not on a baseline. Memory breaks at 90% of the limit when the trend says the limit will be reached within an hour.

With less than an hour of history, only the floors and the fixed rules apply, so a fresh install doesn't fire on noise.

### 5. An incident like any other

A signal incident goes through the cause engine (ADR-0006) like a pod incident: the changes to the app and what it uses in the 30 minutes before, with evidence. "Errors started 40 seconds after the new pods became ready" is the signal's version of "only pods started after the change fail". Every incident says what detected it (source and numbers), and its card shows the signal's chart with the changes marked. An app whose pods are failing and whose errors are up is one incident with two signals.

Firing Alertmanager alerts (ADR-0011) also become incidents on the map, tagged as the user's own alert.

### 6. Recorded with everything else

Signal values are recorded at 1-minute resolution in the recording (one small message per minute), so rewind, replays and post-mortems show them. They carry no names beyond what the map already has. Anonymized replays rename them like every other name.

## Consequences

- kubexray notices what users feel, not only what crashes, and still names the change that likely caused it.
- Without Prometheus, it gets restarts and saturation, not errors or latency. The sources page says what's missing and how to add it.
- Baselines can be wrong, especially just after install and for spiky apps. Floors, sustained times, the sensitivity setting and showing the numbers on every incident keep this visible and adjustable.
- Each new source is an adapter with fixtures from its real metrics. Traefik, ingress-nginx, Envoy, OpenTelemetry and ALB come first, then Linkerd, Kong and HAProxy.
