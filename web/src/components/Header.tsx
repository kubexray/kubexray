import type { StreamStatus } from "../lib/useStream";
import { allWorkloads, type NamespaceView, workloadHealth } from "../lib/view";

const STATUS: Record<StreamStatus, string> = {
  connecting: "Connecting…",
  live: "Live",
  reconnecting: "Reconnecting…",
  "signed-out": "Signed out",
};

export function Header({ view, status, rewound }: { view: NamespaceView[]; status: StreamStatus; rewound: boolean }) {
  const ws = allWorkloads(view);
  const count = (h: string) => ws.filter(({ w }) => workloadHealth(w) === h).length;
  const pods = ws.reduce((n, { w }) => n + w.pods.length, 0);
  const bad = count("crit");
  const updating = count("warn");
  return (
    <header className="header">
      <div className="brand">
        <h1>
          kube<span>xray</span>
        </h1>
        <span className={`status ${rewound ? "rewound" : status}`}>{rewound ? "Rewound" : STATUS[status]}</span>
      </div>
      <div className="counts" aria-live="polite">
        <span>
          <i className="dot ok" />
          <b>{count("ok")}</b> healthy apps
        </span>
        {updating > 0 && (
          <span>
            <b>{updating}</b> updating
          </span>
        )}
        <span className={bad ? "bad" : ""}>
          <i className="dot crit" />
          <b>{bad}</b> need attention
        </span>
        <span>
          <b>{pods}</b> pods
        </span>
      </div>
    </header>
  );
}
