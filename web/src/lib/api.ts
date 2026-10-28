// The server's read-only API (ADR-0007). Requests carry the session cookie
// automatically; they only work from the page kubexray serves.
import type { Change, Incident } from "../gen/cause";
import type { AtResponse, FeedResponse, HistoryResponse, RecommendPreview, Sources } from "../gen/server";

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

/** Where signals come from (ADR-0016). */
export const fetchSources = (signal?: AbortSignal) => get<Sources>("/api/sources", signal);

/** Who is signed in (ADR-0013). Empty without sign-in: token or proxy mode. */
export interface Me {
  email?: string;
  name?: string;
  signOut?: string;
}

export const fetchMe = (signal?: AbortSignal) => get<Me>("/api/me", signal);

/** Where to sign in again, coming back to this page. */
export const signInURL = () => `/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`;

/** One check of a safety recommendation's proof (ADR-0014). */
export interface ProofCheck {
  name: string;
  passed: boolean;
  tried?: number;
  failures?: string[];
}

/** A drafted safety recommendation and its proof (POST /api/recommend). */
export interface Recommendation {
  title: string;
  rationale: string;
  proof: { checks: ProofCheck[] };
  reconstructed?: { key: string; value: string; evidence: string };
  strict: boolean;
  model: string;
  attempts: number;
  usage: { in: number; out: number };
  yaml: string;
}

/** The server's answer when it can't draft, in its own words. */
async function explained<T>(resp: Response): Promise<T> {
  if (resp.status === 401) throw new SignedOutError();
  if (!resp.ok) throw new Error((await resp.text()).trim() || `${resp.status} ${resp.statusText}`);
  return (await resp.json()) as T;
}

const recommendURL = (set: string) => `/api/recommend?set=${encodeURIComponent(set)}`;

/** What drafting a recommendation for a change would send, without sending it. */
export const previewRecommendation = async (set: string, signal?: AbortSignal) =>
  explained<RecommendPreview>(await fetch(recommendURL(set), { signal, credentials: "same-origin" }));

/** Drafts and proves a recommendation for a change. Can take a minute with a local model. */
export const draftRecommendation = async (set: string, signal?: AbortSignal) =>
  explained<Recommendation>(await fetch(recommendURL(set), { method: "POST", signal, credentials: "same-origin" }));
