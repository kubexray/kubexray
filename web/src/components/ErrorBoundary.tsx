import { Component, type ErrorInfo, type ReactNode } from "react";
import { Wordmark } from "./Wordmark";

interface Props {
  children: ReactNode;
  /** What couldn't be shown, for the message: "this replay". */
  what: string;
  /** The way back, e.g. "Open another replay". */
  back: { label: string; onClick: () => void };
}

/** Catches a render error below it (a replay is untrusted; a bug is a bug)
 * and says so plainly, with a way back, instead of a blank page. */
export class ErrorBoundary extends Component<Props, { error: Error | null }> {
  override state = { error: null as Error | null };

  static getDerivedStateFromError(error: unknown) {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  override componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error("kubexray: couldn't render", error, info.componentStack);
  }

  override render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <main className="message landing" role="alert">
        <Wordmark />
        <p>Something went wrong showing {this.props.what}.</p>
        <p className="small">{error.message}</p>
        <div>
          <button
            type="button"
            className="pick"
            onClick={() => {
              this.setState({ error: null });
              this.props.back.onClick();
            }}
          >
            {this.props.back.label}
          </button>
        </div>
      </main>
    );
  }
}
