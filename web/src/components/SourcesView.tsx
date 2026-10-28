import type { Sources } from "../gen/server";
import { ago } from "../lib/format";

const EXPORTERS: { name: string; gives: string; metric: string }[] = [
  { name: "Traefik", gives: "errors and latency per Service", metric: "traefik_service_requests_total" },
  { name: "ingress-nginx", gives: "errors and latency per Ingress backend", metric: "nginx_ingress_controller_requests" },
  { name: "Istio", gives: "errors and latency between apps", metric: "istio_requests_total" },
  { name: "OpenTelemetry", gives: "errors and latency from inside apps", metric: "http_server_request_duration_seconds" },
];

const SENSITIVITY: Record<string, string> = {
  calm: "Calm: a big jump from an app's usual range, held 5 minutes.",
  normal: "Normal: a clear jump from an app's usual range, held 2 minutes.",
  strict: "Strict: a smaller jump, held 1 minute.",
};

interface Row {
  name: string;
  gives: string;
  note: string;
  ok: boolean;
  status: string;
  sub?: boolean;
}

/** Where kubexray's signals come from, and how to add what's missing
 * (ADR-0016). */
export function SourcesView({ sources, now }: { sources: Sources | null; now: number }) {
  if (!sources) return <main className="feed sources" />;
  const prom = !!sources.prometheus;
  const found = new Set(sources.exporters ?? []);
  const rows: Row[] = [
    { name: "Kubernetes", gives: "crashes, pods not ready, restarts", note: "always", ok: true, status: "Connected" },
    {
      name: "metrics-server",
      gives: "memory against each pod's limit",
      note: sources.metricsServer ? "the same numbers as kubectl top" : "not found: install metrics-server, or kubectl top won't work either",
      ok: sources.metricsServer,
      status: sources.metricsServer ? "Connected" : "Not found",
    },
    {
      name: "Prometheus",
      gives: prom ? (sources.prometheus ?? "") : "request errors and latency",
      note: prom ? "found by its Service, or set with signals.prometheusURL" : "not found: set signals.prometheusURL in the Helm chart, or --prometheus-url",
      ok: prom,
      status: prom ? "Connected" : "Not found",
    },
    ...(prom
      ? EXPORTERS.map((e) => ({
          name: e.name,
          gives: e.gives,
          note: e.metric,
          ok: found.has(e.name),
          status: found.has(e.name) ? "Found" : "Not found",
          sub: true,
        }))
      : []),
    {
      name: "Alertmanager",
      gives: "your own alerts become incidents",
      note: sources.alertmanager
        ? sources.lastAlert
          ? `last notification ${ago(sources.lastAlert, now)}`
          : "listening at /api/alertmanager; no notification yet"
        : "add kubexray as a webhook receiver (kubexray serve, the Helm chart)",
      ok: sources.alertmanager,
      status: sources.alertmanager ? (sources.lastAlert ? "Receiving" : "Listening") : "Not set up",
    },
  ];
  return (
    <main className="feed sources">
      <section className="feed-list" aria-label="Sources">
        <h2 className="feed-heading">Where incidents come from</h2>
        <p className="sources-intro">kubexray looks for these on start and every few minutes. Nothing to configure when they're there; without them, you still get everything Kubernetes says.</p>
        <table className="sources-table">
          <thead>
            <tr>
              <th scope="col">Source</th>
              <th scope="col">Gives kubexray</th>
              <th scope="col">Notes</th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.name} className={r.sub ? "sub" : undefined}>
                <th scope="row">{r.sub ? `↳ ${r.name}` : r.name}</th>
                <td>{r.gives}</td>
                <td className="note">{r.note}</td>
                <td className={r.ok ? "ok" : "off"}>{r.status}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="sources-sensitivity">
          <strong>Sensitivity</strong>
          <span>{SENSITIVITY[sources.sensitivity] ?? sources.sensitivity}</span>
          <span className="note">Each app is judged against its own usual range. Change it with signals.sensitivity, or --sensitivity.</span>
        </div>
      </section>
    </main>
  );
}
