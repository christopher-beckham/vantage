/**
 * Does a real agent follow `.vantage/watching.md`? `just agent-eval`.
 *
 * Each scenario builds a fresh git repository, serves it with a real Vantage
 * server, files comments through the HTTP API, and clicks Send the way the
 * browser does: the batch text comes from the review store's own
 * `pendingPayload()`, so the agent reads exactly what a reviewer's Send writes.
 * Then it runs one headless Claude Code session (`claude -p`) told only to
 * watch, and grades what happened: the files on disk, the review the server
 * holds, and the tool calls in the session's transcript.
 *
 * One session answers what is already waiting and ends its turn. A headless
 * session cannot sit idle and be woken by its background wait, so a scenario
 * stops when the turn ends and checks that the wait was started again; whether
 * the loop survives many rounds is out of its reach.
 *
 * This spends money and needs the network (the agent's API, and `uvx` for
 * `vantage-check`), and an agent's behaviour varies, so it never runs in the
 * gate. Design: docs/design/review-outbox.md.
 */
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import { hashBlockText } from "../src/lib/reviewAnchor";
import { newReviewComment, useReviewStore } from "../src/stores/useReviewStore";
import type { ReviewComment } from "../src/types";

const repoRoot = resolve(import.meta.dirname, "../..");
const keep = process.env.VANTAGE_EVAL_KEEP === "1";
const model = process.env.VANTAGE_EVAL_MODEL;
const budgetUsd = process.env.VANTAGE_EVAL_BUDGET_USD ?? "2";
const sessionTimeoutMs = 10 * 60_000;

/**
 * What the agent may run without asking. A headless session has nobody to
 * ask, so anything else is denied and reported: a denial here is a gap in
 * the permission list `watching.md` gives users to allow.
 */
const allowedTools = [
  "Read",
  "Edit",
  "Write",
  "Glob",
  "Grep",
  "Bash(ls:*)",
  "Bash(cat:*)",
  "Bash(mv:*)",
  "Bash(rm:*)",
  "Bash(mkdir:*)",
  "Bash(sort:*)",
  "Bash(pwd)",
  "Bash(until:*)",
  "Bash(git status:*)",
  "Bash(git diff:*)",
  "Bash(uvx vantage-check:*)",
];

const prompt =
  "Watch this repository for Vantage reviews: read .vantage/watching.md and follow it.";

let work = "";
let binary = "";

beforeAll(() => {
  // Resolved, because Vantage names the checkout it serves by its real path
  // and the agent compares that with its working directory: on macOS the
  // temporary directory is reached through a link (/var is /private/var).
  work = realpathSync(mkdtempSync(join(tmpdir(), "vantage-agent-eval-")));
  binary = join(work, "vantage");
  const built = spawnSync("go", ["build", "-o", binary, "./cmd/vantage"], {
    cwd: repoRoot,
    encoding: "utf8",
  });
  if (built.status !== 0) throw new Error(`go build failed:\n${built.stderr}`);
});

afterAll(() => {
  if (keep) console.log(`kept the scenarios in ${work}`);
  else rmSync(work, { recursive: true, force: true });
});

// ---------------------------------------------------------------- the rig ---

interface Doc {
  path: string;
  text: string;
}

interface Note {
  /** The line the comment is anchored on, 1-based. */
  line: number;
  text: string;
}

class Scenario {
  readonly repo: string;
  private readonly home: string;
  private server: ChildProcess | null = null;
  private base = "";

  constructor(name: string) {
    this.repo = join(work, name, "repo");
    this.home = join(work, name, "home");
    mkdirSync(this.repo, { recursive: true });
    mkdirSync(this.home, { recursive: true });
  }

  git(...args: string[]): string {
    const r = spawnSync("git", args, { cwd: this.repo, encoding: "utf8" });
    if (r.status !== 0) throw new Error(`git ${args.join(" ")}: ${r.stderr}`);
    return r.stdout;
  }

  commit(docs: Doc[]) {
    this.git("init", "-q");
    this.git("config", "user.email", "eval@vantage.local");
    this.git("config", "user.name", "Vantage eval");
    for (const d of docs) {
      const full = join(this.repo, d.path);
      mkdirSync(dirname(full), { recursive: true });
      writeFileSync(full, d.text);
    }
    this.git("add", "-A");
    this.git("commit", "-qm", "init");
  }

  async serve() {
    const port = await freePort();
    this.base = `http://127.0.0.1:${port}/api`;
    this.server = spawn(
      binary,
      ["serve", "--port", String(port), "--no-open", this.repo],
      {
        env: {
          ...process.env,
          HOME: this.home,
          USERPROFILE: this.home,
          XDG_CONFIG_HOME: join(this.home, ".config"),
        },
        stdio: ["ignore", "ignore", "pipe"],
      },
    );
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      try {
        if ((await fetch(`${this.base}/health`)).ok) return;
      } catch {
        // not listening yet
      }
      await new Promise((r) => setTimeout(r, 100));
    }
    throw new Error("the Vantage server did not start");
  }

  stop() {
    this.server?.kill();
  }

  async api<T>(method: string, target: string, body?: unknown): Promise<T> {
    const res = await fetch(`${this.base}${target}`, {
      method,
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`${method} ${target}: ${res.status} ${text}`);
    return JSON.parse(text) as T;
  }

  /** Files comments on a document as the comment popover does. */
  async comment(doc: Doc, notes: Note[]): Promise<string[]> {
    const lines = doc.text.split("\n");
    const ids: string[] = [];
    for (const n of notes) {
      const block = paragraphAt(lines, n.line);
      const c = newReviewComment(
        {
          source_line: n.line,
          block_text_hash: hashBlockText(block),
          selection_offset: 0,
          selection_length: 0,
        },
        n.text,
        block,
      );
      await this.api("POST", `/review/comments?path=${enc(doc.path)}`, {
        id: c.id,
        comment: c.comment,
        anchor: c.anchor,
        fallback_text: c.fallback_text,
        created_at: c.created_at,
      });
      ids.push(c.id);
    }
    return ids;
  }

  /** Clicks Send: the store builds the text from the server's review. */
  async send(doc: Doc) {
    const review = await this.review(doc.path);
    useReviewStore.setState({
      filePath: doc.path,
      lastContent: readFileSync(join(this.repo, doc.path), "utf8"),
      comments: review?.comments ?? [],
    });
    const text = useReviewStore.getState().pendingPayload();
    expect(text, "Send has something to send").not.toBeNull();
    await this.api("POST", `/review/outbox?path=${enc(doc.path)}`, { text });
  }

  review(path: string) {
    return this.api<{ comments: ReviewComment[] } | null>(
      "GET",
      `/review?path=${enc(path)}`,
    );
  }

  async agentReplies(path: string, id: string) {
    const r = await this.review(path);
    const c = r?.comments.find((x) => x.id === id);
    return (c?.reactions ?? []).filter((x) => x.actor === "agent");
  }

  outbox(): string[] {
    const dir = join(this.repo, ".vantage", "outbox");
    return existsSync(dir) ? readdirSync(dir).sort() : [];
  }

  read(path: string): string | null {
    const full = join(this.repo, path);
    return existsSync(full) ? readFileSync(full, "utf8") : null;
  }

  /** Runs one headless session and returns its transcript. */
  agent(): Promise<Session> {
    const args = [
      "-p",
      prompt,
      "--output-format",
      "stream-json",
      "--verbose",
      "--setting-sources",
      "project,local",
      "--strict-mcp-config",
      "--no-session-persistence",
      "--max-budget-usd",
      budgetUsd,
      "--allowedTools",
      ...allowedTools,
    ];
    if (model) args.push("--model", model);
    return runSession(this.repo, args, join(this.repo, "..", "session.jsonl"));
  }
}

// ------------------------------------------------------------ the session ---

interface ToolCall {
  name: string;
  input: Record<string, unknown>;
}

interface Session {
  calls: ToolCall[];
  result: {
    total_cost_usd?: number;
    num_turns?: number;
    is_error?: boolean;
    result?: string;
    permission_denials?: { tool_name: string; tool_input: unknown }[];
  } | null;
}

/**
 * Runs claude and collects its tool calls. When the turn ends the session may
 * linger on its background wait, so the whole process group is stopped then,
 * and the wait with it.
 */
function runSession(
  cwd: string,
  args: string[],
  log: string,
): Promise<Session> {
  return new Promise((done, fail) => {
    const child = spawn("claude", args, {
      cwd,
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const session: Session = { calls: [], result: null };
    const lines: string[] = [];
    let buf = "";
    const stopAll = () => {
      try {
        process.kill(-child.pid!, "SIGTERM");
      } catch {
        // already gone
      }
    };
    const timer = setTimeout(() => {
      stopAll();
      fail(
        new Error(`the session ran past ${sessionTimeoutMs / 60_000} minutes`),
      );
    }, sessionTimeoutMs);
    child.stdout!.setEncoding("utf8");
    child.stdout!.on("data", (chunk: string) => {
      buf += chunk;
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        lines.push(line);
        let ev: {
          type?: string;
          message?: {
            content?: {
              type: string;
              name?: string;
              input?: Record<string, unknown>;
            }[];
          };
        };
        try {
          ev = JSON.parse(line);
        } catch {
          continue;
        }
        if (ev.type === "assistant") {
          for (const part of ev.message?.content ?? []) {
            if (part.type === "tool_use") {
              session.calls.push({
                name: part.name ?? "",
                input: part.input ?? {},
              });
            }
          }
        }
        if (ev.type === "result") {
          session.result = ev as Session["result"];
          clearTimeout(timer);
          setTimeout(stopAll, 2_000);
        }
      }
    });
    let stderr = "";
    child.stderr!.on("data", (d) => (stderr += String(d)));
    child.on("exit", () => {
      clearTimeout(timer);
      writeFileSync(log, lines.join("\n") + "\n");
      if (session.result === null) {
        fail(new Error(`the session ended without a result:\n${stderr}`));
      } else {
        done(session);
      }
    });
  });
}

const commandOf = (c: ToolCall) => String(c.input.command ?? "");
const pathOf = (c: ToolCall) => String(c.input.file_path ?? "");

/** The index of the first call that renames a batch to `.claimed`. */
function firstClaim(s: Session, batch: string): number {
  return s.calls.findIndex(
    (c) =>
      c.name === "Bash" &&
      commandOf(c).includes(batch) &&
      /\bmv\b/.test(commandOf(c)) &&
      commandOf(c).includes(".claimed"),
  );
}

/** The index of the first call that reads a batch under its ready name. */
function firstUnclaimedRead(s: Session, batch: string): number {
  return s.calls.findIndex((c) => {
    if (c.name === "Read") return pathOf(c).endsWith(batch);
    if (c.name !== "Bash") return false;
    const cmd = commandOf(c);
    return (
      /\b(cat|head|less|sed)\b/.test(cmd) &&
      new RegExp(`${esc(batch)}(?!\\.)`).test(cmd)
    );
  });
}

/** Whether the last thing the session ran was the background wait. */
function restartedTheWait(s: Session): boolean {
  const waits = s.calls
    .map((c, i) => ({ c, i }))
    .filter(
      ({ c }) =>
        c.name === "Bash" &&
        c.input.run_in_background === true &&
        commandOf(c).includes("until ls .vantage/outbox"),
    );
  if (waits.length === 0) return false;
  const last = waits[waits.length - 1]!.i;
  return s.calls
    .slice(last + 1)
    .every((c) => c.name !== "Edit" && c.name !== "Write");
}

function report(name: string, s: Session) {
  const r = s.result;
  console.log(
    `[${name}] ${s.calls.length} tool calls, ${r?.num_turns ?? "?"} turns, ` +
      `$${(r?.total_cost_usd ?? 0).toFixed(2)}` +
      (r?.permission_denials?.length
        ? `, denied: ${r.permission_denials.map((d) => `${d.tool_name} ${JSON.stringify(d.tool_input)}`).join("; ")}`
        : ""),
  );
}

// ------------------------------------------------------------- scenarios ---

const releaseDoc: Doc = {
  path: "guides/release-process.md",
  text: [
    "# Release process",
    "",
    "Releases go out every other Tuesday. The release captain cuts a branch,",
    "runs the smoke tests, and posts in the team channel when it is live.",
    "",
    "If the smoke tests fail, the captain decides whether to fix forward or",
    "skip the release. There is no written rule for this.",
    "",
  ].join("\n"),
};

const onboardingDoc: Doc = {
  path: "onboarding.md",
  text: [
    "# Onboarding plan",
    "",
    "New engineers spend most of their first week setting up their laptop.",
    "",
    "## Week one",
    "",
    "- Day 1: laptop and accounts.",
    "- Day 2: read the architecture overview.",
    "",
  ].join("\n"),
};

describe("an agent watching for Vantage reviews", () => {
  test("answers a batch: claims it first, edits only the document, delivers, waits again", async () => {
    const sc = new Scenario("round");
    sc.commit([releaseDoc]);
    await sc.serve();
    try {
      const [who, rule] = await sc.comment(releaseDoc, [
        {
          line: 3,
          text: "Say who picks the release captain: it is the team lead.",
        },
        {
          line: 6,
          text: "Write the rule down: if the smoke tests fail, skip the release and ship with the next one.",
        },
      ]);
      await sc.send(releaseDoc);
      const [batch] = sc.outbox();
      expect(batch).toMatch(/\.md$/);

      const s = await sc.agent();
      report("round", s);

      const claim = firstClaim(s, batch!);
      const read = firstUnclaimedRead(s, batch!);
      expect
        .soft(claim, "renamed the batch to .claimed")
        .toBeGreaterThanOrEqual(0);
      expect
        .soft(read === -1 || read > claim, "claimed before reading")
        .toBe(true);

      expect
        .soft(
          await sc.agentReplies(releaseDoc.path, who!),
          "replied to the first comment",
        )
        .toHaveLength(1);
      expect
        .soft(
          await sc.agentReplies(releaseDoc.path, rule!),
          "replied to the second comment",
        )
        .toHaveLength(1);
      const text = sc.read(releaseDoc.path) ?? "";
      expect.soft(text, "made the first change").toMatch(/team lead/i);
      expect
        .soft(text, "made the second change")
        .not.toMatch(/no written rule/i);

      expect
        .soft(
          sc.git("status", "--porcelain").trim(),
          "changed only the document",
        )
        .toBe(`M ${releaseDoc.path}`);
      expect.soft(sc.outbox(), "deleted its claim").toEqual([]);
      expect
        .soft(restartedTheWait(s), "ended by starting the wait again")
        .toBe(true);
      expect
        .soft(
          s.result?.permission_denials ?? [],
          "needed nothing outside the list",
        )
        .toEqual([]);
    } finally {
      sc.stop();
    }
  });

  test("answers two waiting batches, oldest first", async () => {
    const sc = new Scenario("queue");
    sc.commit([releaseDoc, onboardingDoc]);
    await sc.serve();
    try {
      const [first] = await sc.comment(onboardingDoc, [
        {
          line: 3,
          text: "Say that setup takes a day at most once the new script lands.",
        },
      ]);
      await sc.send(onboardingDoc);
      await new Promise((r) => setTimeout(r, 5));
      const [second] = await sc.comment(releaseDoc, [
        { line: 3, text: "Say that releases go out at 10am." },
      ]);
      await sc.send(releaseDoc);
      const [older, newer] = sc.outbox();
      expect(older).toContain("onboarding.md");

      const s = await sc.agent();
      report("queue", s);

      const a = firstClaim(s, older!);
      const b = firstClaim(s, newer!);
      expect.soft(a, "claimed the older batch").toBeGreaterThanOrEqual(0);
      expect.soft(b, "claimed the newer batch").toBeGreaterThanOrEqual(0);
      expect.soft(a < b, "oldest first").toBe(true);
      expect
        .soft(await sc.agentReplies(onboardingDoc.path, first!))
        .toHaveLength(1);
      expect
        .soft(await sc.agentReplies(releaseDoc.path, second!))
        .toHaveLength(1);
      expect.soft(sc.outbox(), "deleted both claims").toEqual([]);
      expect
        .soft(restartedTheWait(s), "ended by starting the wait again")
        .toBe(true);
      expect.soft(s.result?.permission_denials ?? []).toEqual([]);
    } finally {
      sc.stop();
    }
  });

  test("reports a batch it cannot answer, with a reason, and does not invent the document", async () => {
    const sc = new Scenario("failure");
    sc.commit([releaseDoc]);
    await sc.serve();
    try {
      const [id] = await sc.comment(releaseDoc, [
        {
          line: 3,
          text: "Say who picks the release captain: it is the team lead.",
        },
      ]);
      await sc.send(releaseDoc);
      const [batch] = sc.outbox();
      unlinkSync(join(sc.repo, releaseDoc.path));

      const s = await sc.agent();
      report("failure", s);

      expect
        .soft(sc.outbox(), "left the batch failed, with its reason")
        .toEqual([`${batch}.failed`, `${batch}.failed.reason`]);
      const reason = sc.read(`.vantage/outbox/${batch}.failed.reason`) ?? "";
      expect
        .soft(reason.trim().length, "the reason says something")
        .toBeGreaterThan(10);
      expect
        .soft(sc.read(releaseDoc.path), "did not recreate the document")
        .toBeNull();
      expect
        .soft(await sc.agentReplies(releaseDoc.path, id!), "sent no reply")
        .toHaveLength(0);
      expect
        .soft(restartedTheWait(s), "ended by starting the wait again")
        .toBe(true);
      expect.soft(s.result?.permission_denials ?? []).toEqual([]);
    } finally {
      sc.stop();
    }
  });
});

// --------------------------------------------------------------- helpers ---

/** The text of the paragraph or list item holding 1-based `line`. */
function paragraphAt(lines: string[], line: number): string {
  let start = line - 1;
  let end = line - 1;
  while (
    start > 0 &&
    lines[start - 1]!.trim() !== "" &&
    !lines[start]!.startsWith("- ")
  )
    start--;
  while (
    end + 1 < lines.length &&
    lines[end + 1]!.trim() !== "" &&
    !lines[end + 1]!.startsWith("- ")
  )
    end++;
  return lines
    .slice(start, end + 1)
    .join(" ")
    .replace(/^(#+|-)\s+/, "");
}

function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const srv = createServer();
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      srv.close(() =>
        typeof addr === "object" && addr
          ? done(addr.port)
          : fail(new Error("no port")),
      );
    });
  });
}

function enc(s: string) {
  return encodeURIComponent(s);
}

function esc(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
