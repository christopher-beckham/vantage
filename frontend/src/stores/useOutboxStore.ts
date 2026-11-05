import { create } from "zustand";
import axios from "axios";
import { isStaticMode } from "../lib/staticMode";
import { useRepoStore } from "./useRepoStore";

/**
 * The review outbox (docs/design/review-outbox.md): what has become of each
 * batch of comments sent to a watching agent, for the document on screen.
 *
 * The server owns the files; this store only mirrors one document's list. It is
 * fetched when the review panel shows a document, after a Send, and whenever
 * the server pushes `outbox_changed`, which it does on every rename an agent
 * makes in the outbox. A static export has no outbox, so every action is a
 * no-op there.
 */

/** One batch, as `GET /review/outbox` reports it. */
export interface OutboxBatch {
  name: string;
  state: "sent" | "claimed" | "failed";
  /** Unix milliseconds. */
  sent_at: number;
  reason?: string;
}

interface OutboxState {
  /** The document `batches` belongs to, or null before any load. */
  path: string | null;
  batches: OutboxBatch[];
  sending: boolean;
  /** The last Send or load that failed, shown in the panel. */
  error: string | null;
  load: (path: string) => Promise<void>;
  /** Fetch the current document's list again, after an `outbox_changed` push. */
  reload: () => Promise<void>;
  /** Write `text` as a new batch for `path`. Resolves to whether it landed. */
  send: (path: string, text: string) => Promise<boolean>;
}

const apiBase = (): string | null => {
  const { currentRepo, isMultiRepo } = useRepoStore.getState();
  if (isMultiRepo) {
    if (!currentRepo) return null;
    return `/api/r/${encodeURIComponent(currentRepo)}`;
  }
  return "/api";
};

/**
 * Discards a response that lost a race: a push-triggered reload, a Send's own
 * reply and a load for the next document can all be in flight at once.
 */
let seq = 0;

const errorText = (e: unknown, fallback: string): string => {
  const data = (e as { response?: { data?: { error?: unknown } } } | null)
    ?.response?.data;
  return data && typeof data.error === "string" && data.error
    ? data.error
    : fallback;
};

export const useOutboxStore = create<OutboxState>((set, get) => ({
  path: null,
  batches: [],
  sending: false,
  error: null,

  load: async (path) => {
    const base = apiBase();
    if (!base || isStaticMode()) return;
    const mine = ++seq;
    if (get().path !== path) set({ path, batches: [], error: null });
    try {
      const res = await axios.get<{ batches: OutboxBatch[] }>(
        `${base}/review/outbox`,
        { params: { path } },
      );
      if (mine !== seq) return;
      set({ path, batches: res?.data?.batches ?? [] });
    } catch (e) {
      if (mine !== seq) return;
      set({ error: errorText(e, "Could not read the outbox") });
    }
  },

  reload: async () => {
    const { path } = get();
    if (path !== null) await get().load(path);
  },

  send: async (path, text) => {
    const base = apiBase();
    if (!base || isStaticMode()) return false;
    const mine = ++seq;
    set({ sending: true, error: null });
    try {
      const res = await axios.post<{ batches: OutboxBatch[] }>(
        `${base}/review/outbox`,
        { text },
        { params: { path } },
      );
      if (mine === seq) set({ path, batches: res?.data?.batches ?? [] });
      return true;
    } catch (e) {
      if (mine === seq) set({ error: errorText(e, "Send failed") });
      return false;
    } finally {
      set({ sending: false });
    }
  },
}));

/**
 * What the panel says about the newest batch, or null when there is nothing to
 * say. "Not claimed yet" is a status, not a warning: the agent may be busy with
 * other work in its session (review-outbox.md D3), and Vantage cannot tell a
 * busy agent from an absent one.
 */
export function outboxStatus(
  batches: readonly OutboxBatch[],
): { tone: "info" | "working" | "failed"; text: string } | null {
  const failed = batches.find((b) => b.state === "failed");
  if (failed) {
    return {
      tone: "failed",
      text: failed.reason
        ? `The agent couldn't answer: ${failed.reason}`
        : "The agent couldn't answer this batch",
    };
  }
  if (batches.some((b) => b.state === "claimed")) {
    return { tone: "working", text: "Agent is working on this document" };
  }
  if (batches.some((b) => b.state === "sent")) {
    return { tone: "info", text: "Sent, not claimed yet" };
  }
  return null;
}

/**
 * Whether a batch for this document is still waiting to be claimed. Send stays
 * off then, so a second click does not queue the same comments twice; once an
 * agent has claimed it, a new Send is a new round (D1).
 */
export function hasUnclaimedBatch(batches: readonly OutboxBatch[]): boolean {
  return batches.some((b) => b.state === "sent");
}
