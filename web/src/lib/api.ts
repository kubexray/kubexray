// The server's read-only API (ADR-0007). Requests carry the session cookie
// automatically; they only work from the page kubexray serves.
import type { Change, Incident } from "../gen/cause";
import type { AtResponse, FeedResponse, HistoryResponse } from "../gen/server";

export class SignedOutError extends Error {
  constructor() {
    super("Open kubexray with the link it printed in your terminal.");
    this.name = "SignedOutError";
  }
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T | null> {
  const resp = await fetch(path, { signal, credentials: "same-origin" });
  if (resp.status === 401) throw new SignedOutError();
  if (resp.status === 404 || resp.status === 503) return null;
  if (!resp.ok) throw new Error(`${path}: ${resp.status} ${await resp.text()}`);
  return (await resp.json()) as T;
}

export const fetchHistory = (signal?: AbortSignal) => get<HistoryResponse>("/api/history", signal);

export const fetchAt = (t: string, signal?: AbortSignal) => get<AtResponse>(`/api/at?t=${encodeURIComponent(t)}`, signal);

export const fetchWhy = (id: string, signal?: AbortSignal) => get<Incident>(`/api/why?id=${encodeURIComponent(id)}`, signal);

export const fetchChanges = (signal?: AbortSignal) => get<Change[]>("/api/changes", signal);

export const fetchFeed = (signal?: AbortSignal) => get<FeedResponse>("/api/feed", signal);

/** Who is signed in (ADR-0013). Empty without sign-in: token or proxy mode. */
export interface Me {
  email?: string;
  name?: string;
  signOut?: string;
}

export const fetchMe = (signal?: AbortSignal) => get<Me>("/api/me", signal);

/** Where to sign in again, coming back to this page. */
export const signInURL = () => `/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`;
