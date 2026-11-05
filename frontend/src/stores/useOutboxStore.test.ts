import { describe, it, expect, vi, beforeEach } from "vitest";
import axios from "axios";
import {
  hasUnclaimedBatch,
  outboxStatus,
  useOutboxStore,
  type OutboxBatch,
} from "./useOutboxStore";

vi.mock("axios");
const mockedAxios = vi.mocked(axios, true);

const batch = (over: Partial<OutboxBatch> = {}): OutboxBatch => ({
  name: "0001762300000123-a.md-00000000.md",
  state: "sent",
  sent_at: 1762300000123,
  ...over,
});

describe("useOutboxStore", () => {
  beforeEach(() => {
    useOutboxStore.setState({
      path: null,
      batches: [],
      sending: false,
      error: null,
    });
    vi.clearAllMocks();
  });

  it("loads a document's batches", async () => {
    mockedAxios.get.mockResolvedValueOnce({ data: { batches: [batch()] } });

    await useOutboxStore.getState().load("a.md");

    expect(mockedAxios.get).toHaveBeenCalledWith("/api/review/outbox", {
      params: { path: "a.md" },
    });
    expect(useOutboxStore.getState()).toMatchObject({
      path: "a.md",
      batches: [batch()],
    });
  });

  it("drops a slower load for the document the reader left", async () => {
    let finishFirst: (v: unknown) => void = () => {};
    mockedAxios.get
      .mockReturnValueOnce(new Promise((r) => (finishFirst = r)))
      .mockResolvedValueOnce({ data: { batches: [] } });

    const first = useOutboxStore.getState().load("a.md");
    await useOutboxStore.getState().load("b.md");
    finishFirst({ data: { batches: [batch()] } });
    await first;

    expect(useOutboxStore.getState()).toMatchObject({
      path: "b.md",
      batches: [],
    });
  });

  it("sends the text and adopts the server's list", async () => {
    mockedAxios.post.mockResolvedValueOnce({ data: { batches: [batch()] } });

    const ok = await useOutboxStore.getState().send("a.md", "## Comments");

    expect(ok).toBe(true);
    expect(mockedAxios.post).toHaveBeenCalledWith(
      "/api/review/outbox",
      { text: "## Comments" },
      { params: { path: "a.md" } },
    );
    expect(useOutboxStore.getState()).toMatchObject({
      batches: [batch()],
      sending: false,
      error: null,
    });
  });

  it("shows the server's reason when a send fails", async () => {
    mockedAxios.post.mockRejectedValueOnce({
      response: { data: { error: "Nothing to send" } },
    });

    const ok = await useOutboxStore.getState().send("a.md", " ");

    expect(ok).toBe(false);
    expect(useOutboxStore.getState()).toMatchObject({
      error: "Nothing to send",
      sending: false,
    });
  });
});

describe("outboxStatus", () => {
  it("says nothing when there are no batches", () => {
    expect(outboxStatus([])).toBeNull();
  });

  it("reports a sent batch as a status, not a warning", () => {
    expect(outboxStatus([batch()])).toEqual({
      tone: "info",
      text: "Sent, not claimed yet",
    });
  });

  it("reports a claimed batch as work in progress", () => {
    expect(outboxStatus([batch(), batch({ state: "claimed" })])?.tone).toBe(
      "working",
    );
  });

  it("puts a failure, with the agent's reason, ahead of everything", () => {
    expect(
      outboxStatus([
        batch({ state: "claimed" }),
        batch({ state: "failed", reason: "a.md was deleted" }),
      ]),
    ).toEqual({
      tone: "failed",
      text: "The agent couldn't answer: a.md was deleted",
    });
  });
});

describe("hasUnclaimedBatch", () => {
  it("is true only while a batch waits to be claimed", () => {
    expect(hasUnclaimedBatch([batch()])).toBe(true);
    expect(hasUnclaimedBatch([batch({ state: "claimed" })])).toBe(false);
    expect(hasUnclaimedBatch([])).toBe(false);
  });
});
