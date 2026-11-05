---
title: "The review outbox, step by step"
status: draft # draft | in-review | accepted | deprecated
date: 2026-11-05
tags: [review, agents, inbox, outbox, tutorial]
summary: "A walk through the review outbox from the simplest idea to the races and gaps, with a timeline diagram for each step."
---

# The review outbox, step by step

This is a teaching companion to [the design](review-outbox.md). The design
records what was decided. This document explains how the pieces work together,
one idea at a time, starting with the simplest. Each section builds on the one
before it.

Every diagram reads top to bottom, with time going down, like a TCP handshake
diagram. Each column is one participant, and each arrow is one thing that
participant does to another. A number on the left marks a step the text refers
to. A note beside a column is something that participant does or sees on its own.

Where the code is built and where it is only planned, the section says so. As of
`a750b64`, build steps 1 and 2 are done: the guide, the watcher, Send and the
states. Steps 3 to 6 are not built.

## 1. Two trips, and which one changes

A review round sends the reviewer's comments to the agent, then brings the
agent's replies back. The trip back has been a file for some time. This design
makes the trip out a file too.

| Trip | Before | With the outbox |
| :--- | :--- | :--- |
| Comments, from Vantage to the agent | Copy, then paste into the session | Send writes `.vantage/outbox/x.md` |
| Replies, from the agent to Vantage | The agent writes `.vantage/inbox/y.jsonl` | Unchanged |

Nothing new is talking over a network. Both trips are files in the repository,
which is why the design still satisfies P1: the filesystem is the only channel.
Copy stays, for agents that are not watching.

## 2. Handing over a file without the reader seeing half of it

Building block: **write the file under a scratch name, then rename it.**

A writer that creates `x.md` and fills it in place can be read halfway through.
The fix is to write all of it to a name the reader ignores, then rename it to
the real name:

```text
writer                                   reader
  |                                         |
  | write x.md.writing                      |  ignores it: wrong name
  | (takes a while, may be half written)    |
  |                                         |
  | rename x.md.writing to x.md             |
  |---------------------------------------->|  sees x.md, complete
```

A rename inside one directory happens all at once. Before it, the name `x.md`
does not exist. After it, the name points to the finished file. There is no
moment in between, so a file under its final name is always complete.

Both directories use this. Vantage writes `x.md.writing` and renames it to
`x.md` in the outbox. The agent writes a scratch file and renames it to
`y.jsonl` in the inbox. You will see renames again in section 5, used for a
second purpose.

## 3. Waiting: "is there a file?", not "did something change?"

The agent waits with one shell command, run in the background:

```bash
sh .vantage/wait-for-batch.sh
```

That script, which Vantage writes beside `watching.md`, is a loop:

```bash
until ls .vantage/outbox 2>/dev/null | grep -q '\.md$'; do
	sleep 1
done
```

Once a second it asks whether any file ending in `.md` is in the outbox. When
one is, the command exits. In Claude Code, a background command exiting wakes
the session.

It matters that the question is "does a file exist?" and not "did a file just
appear?". A batch sent while nobody was listening is still found later:

```text
time    outbox/                 Agent
  |                               |
  | x.md arrives                  |  busy with other work, no wait running
  |                               |
  |    (x.md sits there)          |
  |                               |
  |                               |  finishes, starts the wait
  |------------------------------>|  the first ls finds x.md: exits at once
```

If the wait had been watching for changes, the change would have happened
before it started, and it would have missed it. So no batch is lost because the
agent was busy, or because no session was open.

The pattern `\.md$`, "ends in `.md`", matters too. Every other state of a batch
adds something to the end of the name (`x.md.claimed`, `x.md.failed`), and none
of those end in `.md`. Only a batch that is ready to be answered wakes the
agent. The rest of this document writes that as `*.md`.

Why a script, and not the loop typed straight into the shell? Two trials. The
first version asked for `.vantage/outbox/*.md` directly, and in zsh a pattern
that matches nothing is an error from the shell itself, so it printed "no
matches found" every second: 38,000 lines in a day-long trial. The second,
listing the directory and filtering the names as above, fixed that but could not
be allowed by a permission rule: Claude Code cannot parse a pipe inside an
`until` condition, so in its standard permission mode it asked before every
round. One plain command, `sh .vantage/wait-for-batch.sh`, is allowed by one
rule and runs under `sh` whatever the user's shell is.

## 4. One whole round

Now put sections 2 and 3 together. This is a round that goes well:

```text
    Reviewer         Vantage                outbox/                        Agent                        inbox/
        |               |                      |                             | waiting in the background   |
1       |---- Send ---->|                      |                             |                             |
2       |               |---- write x.md ----->|                             |                             |
3       |               |                      |------ the wait exits ------>|                             |
4       |               |                      |<- rename to x.md.claimed ---|                             |
5       |               |<----- sees it -------|                             |                             |
        |<-- Working ---|                      |                             |                             |
6       |               |                      |                             | edits the document          |
        |               |                      |                             | runs vantage-check          |
7       |               |                      |                             |------- write y.jsonl ------>|
8       |               |<------------------------------ consume and apply --------------------------------|
        |<-- replies ---|                      |                             |                             |
9       |               |                      |<--- delete x.md.claimed ----|                             |
10      |               |                      |                             | starts the wait again       |
        |               |                      |                             | ends its turn               |
```

1. The reviewer clicks Send.
2. Vantage writes the batch with the scratch-then-rename handoff from section 2.
3. The agent's wait sees a `*.md` file and exits, which wakes the session.
4. The agent renames the batch before it reads it (section 5 explains why).
5. Vantage's file watcher sees that rename and tells the browser, and the bar
   changes to Working.
6. The agent edits the document and checks it.
7. The agent delivers its replies through the inbox, as it always has.
8. Vantage consumes the delivery and shows the replies inline.
9. The agent deletes its claimed batch, which ends the round.
10. The agent starts the wait again and ends its turn. Section 6 is about this
    step, because forgetting it is the easiest way for the whole thing to stop.

## 5. The file name is the state

A batch has no database row and no status field. Its state is its name. Here is
what one name says:

```text
1762284223377-guides__release-process.md-69c347d4.md
|___________| |________________________| |______||_|
      |                   |                 |     |
      |                   |                 |     +----- ".md": ready to claim
      |                   |                 +----------- the first 8 hex digits of sha256(path)
      |                   +----------------------------- the document's path, with "/" as "__"
      +------------------------------------------------- when it was sent, unix milliseconds
```

The time stamp comes first so that sorting the names alphabetically sorts them
oldest first. The hash is there because two different paths can flatten to the
same text: `a/b.md` and `a__b.md` both become `a__b.md`. The hash tells them
apart.

A suffix added after `.md` says where the batch is. The usual path is a straight
line:

```text
x.md.writing --rename--> x.md --agent claims--> x.md.claimed --answered--> deleted
```

Every move a batch can make:

| From | What happens | To |
| :--- | :--- | :--- |
| nothing | Vantage writes the batch and renames it | `x.md` |
| `x.md` | Vantage claims it to rewrite it | `x.md.updating` |
| `x.md.updating` | Rewritten | `x.md` |
| `x.md.updating` | No comments left | deleted |
| `x.md` | An agent claims it | `x.md.claimed` |
| `x.md.claimed` | Answered | deleted |
| `x.md.claimed` | The agent can't answer it | `x.md.failed` |
| `x.md.failed` | The reviewer clicks Re-send | `x.md` |
| `x.md.failed` | The reviewer clicks Dismiss | deleted |

What each name tells the reviewer:

| Name | State | What the reviewer sees |
| :--- | :--- | :--- |
| `x.md` | Sent, nobody has it yet | "Sent, not claimed yet" |
| `x.md.updating` | Vantage is rewriting it | Still "Sent"; it lasts milliseconds |
| `x.md.claimed` | An agent is working on it | "Agent is working on this document" |
| `x.md.failed` with `x.md.failed.reason` | The agent gave up | The reason |
| No file, replies arrived | Done | The replies |

Built today: `x.md`, `x.md.claimed` and `x.md.failed`, and the panel and bar
show them. Planned: `x.md.updating` (build step 4), and the Re-send and Dismiss
buttons (step 5).

Vantage never has to ask the agent anything. It reads a directory listing, and
the names tell it everything.

## 6. Claiming: a rename as a lock

The first use of a rename was to make a file appear complete (section 2). Its
second use is to decide **who owns a batch**.

If two processes both try to rename the same `x.md`, the filesystem lets exactly
one succeed. The other gets "file not found", because the first rename took the
name away. So whoever renames `x.md` first owns it, and everyone else knows they
lost.

Here two agent sessions are watching the same repository and wake at once:

```text
    Agent 1                    outbox/                    Agent 2
       |                          | x.md                     |
1      |------ rename x.md ------>|                          |
2      |                          |<----- rename x.md -------|
       |                          | x.md.claimed             |
3      |<------ ok: yours --------|                          |
4      |                          |------- not found ------->|
       |                          |                          | back to step 1
```

1. Agent 1's rename runs first.
2. Agent 2's rename runs a moment later.
3. Agent 1 succeeds and answers the batch.
4. Agent 2 is told the file is gone. Its instructions say that means someone
   else has it, so it goes back to listing the outbox and waiting.

Only one session answers, so nothing is answered twice. The problem left over is
which one: it might be the session that has never seen the document. That is why
the guide says to run one watching session per repository (case 6 in the
design).

Claiming is built, and the agent does it today, following `watching.md`.

## 7. Why Vantage claims a batch before rewriting it

*Planned, build step 4.* Suppose the reviewer edits a comment after clicking
Send, while the batch is still waiting. Vantage should rewrite the batch so the
agent gets the edited text. The obvious way has a gap:

```text
    Vantage                            outbox/                          Agent
1      |------ is x.md unclaimed? ------->|                               |
       |<------------- yes ---------------|                               |
2      |                                  |<-- rename to x.md.claimed ----|
3      |-------- write new x.md --------->|                               |
4      |                                  | x.md.claimed + x.md           |
       |                                  | same comments twice           |
```

1. Vantage checks that `x.md` is still unclaimed. It is.
2. In the gap between the check and the write, the agent claims it.
3. Vantage writes the new `x.md`, believing nobody has the old one.
4. Now there are two batches holding the same comments, and they get answered
   twice.

Checking, then acting, leaves a gap that something else can fall into. The fix
is to make the check and the act one step. Vantage uses the same lock the agents
use: it renames `x.md` to `x.md.updating`. Then there are only two possible
orders, and both are safe.

**Vantage renames first.** The agent's claim fails and it goes back to waiting.
When Vantage renames the rewritten batch back to `x.md`, the agent's wait finds
it and the agent claims the new version:

```text
    Vantage                            outbox/                          Agent
1      |---- rename to x.md.updating ---->|                               |
       |<----- ok: Vantage owns it -------|                               |
2      |                                  |<-------- rename x.md ---------|
       |                                  |---------- not found --------->|
       |                                  |                               | back to step 1
3      |---- rewrite, rename to x.md ---->|                               |
4      |                                  |------- the wait exits ------->|
       |                                  |<-- rename to x.md.claimed ----|
```

**The agent renames first.** Vantage's rename fails, so it knows the agent
already has the old text. It does not touch that batch. The edited comment goes
out as a new batch with only that comment in it, and the agent answers both in
order. If the reviewer deleted a comment instead of editing it, Vantage sends
nothing: the agent's reply to a comment that no longer exists is rejected, as it
is with Copy today.

```text
    Vantage                            outbox/                          Agent
1      |                                  |<-- rename to x.md.claimed ----|
2      |---- rename to x.md.updating ---->|                               |
       |<---------- not found ------------|                               |
3      |---- write z.md: edited one ----->|                               |
4      |                                  |                               | answers x, then z
```

The `x.md.updating` name does not match `*.md`, so the batch is invisible to
waiting agents for the milliseconds the rewrite takes. That is why the panel
still shows it as Sent.

## 8. Keeping the watch alive

A background command wakes the session once, when it exits. After that there is
nothing running, so nothing will wake the agent for the next batch. The agent
has to start the wait again at the end of every round.

The risk is that it forgets. A long conversation is summarized, and an
instruction given at the start can be lost from the summary. So the
instruction to start the wait again is not only in the setup. Vantage adds it to
the end of **every batch**, so it is the last thing the agent reads each round:

```text
you             Agent session                                 outbox/
 |                    |                                          |
 | "watch reviews"    |                                          |
 |------------------->| reads .vantage/watching.md               |
 |                    | starts the wait, ends its turn           |
 |                    |                                          |
 |                    |                       (idle, any length) |
 |                    |                                          |
 |                    |<---------------------------- x.md -------|  wake 1
 |                    | claims, answers, delivers                |
 |                    | last lines of x.md: "start the wait again"
 |                    | starts the wait, ends its turn           |
 |                    |                                          |
 |                    |<---------------------------- x2.md ------|  wake 2
 |                    | ...the same again                        |
```

What starts the first wait is a skill you install and run once per session
(planned, build step 3). In the trial, you started it by asking me in the
conversation.

The design has no timer that wakes the agent every so often as a backup. A
timer slow enough to cost little, such as every 20 minutes, is far too slow to
help a reviewer who is waiting. If the agent does stop watching, the reviewer
finds out within seconds instead, because the bar stays at "Sent, not claimed
yet".

## 9. When a batch can't be answered

Sometimes the agent cannot answer. The document might have been moved, the
batch might be unreadable, or the agent might not be allowed to edit the file.
Two rules make this safe.

**Rule 1: claim before reading.** Look at what happens without it, when the bad
batch stays named `x.md`:

```text
without claiming first                     claiming first

wait exits: x.md exists                    wait exits: x.md exists
read it, can't answer                      rename to x.md.claimed
start the wait again                       read it, can't answer
wait exits at once: x.md still there       report the failure (rule 2)
read it, can't answer                      start the wait again
start the wait again                       wait sleeps: no *.md file
wait exits at once ...
  (forever, and nothing else gets done)
```

Renaming the batch first takes it out of `*.md`, so a bad batch wakes the agent
once, not every second.

**Rule 2: write the reason, then rename.** The agent writes
`x.md.failed.reason` first and renames `x.md.claimed` to `x.md.failed` second.
Vantage shows a failure the moment it sees the `.failed` name. If the rename came
first, Vantage could look in the gap before the reason existed and show a
failure with no explanation.

```text
    Agent                               outbox/                      Vantage
1     |---- write x.md.failed.reason ----->|                            |
2     |------ rename to x.md.failed ------>|                            |
3     |                                    |---- sees x.md.failed ----->|
      |                                    |<---- read the reason ------|
      |                                    |                            | shows it
```

This is the same kind of ordering as section 2: make the thing the reader looks
for appear last, after everything it depends on is in place.

Built: the agent's side (it is in `watching.md`) and Vantage showing the reason.
Planned, build step 5: the Re-send button, which renames `x.md.failed` back to
`x.md`, and Dismiss, which deletes it. Vantage never re-sends a batch by
itself, because it cannot know whether the cause was fixed.

## 10. Who builds the batch's text

The batch is text the browser builds and Go writes to disk:

```text
    Browser                              Go server                     outbox/
1      | pendingPayload()                    |                            |
2      |-------- POST /review/outbox ------->|                            |
3      |                                     | WriteBatch: adds footer    |
       |                                     |------ x.md.writing ------->|
       |                                     |----- rename to x.md ------>|
4      |                                     |<---- watcher sees it ------|
       |<--------- outbox_changed -----------|                            |
5      |-------- GET /review/outbox -------->|                            |
       |<-------- batches + states ----------|                            |
```

1. The browser builds the text with `pendingPayload()`, the same function Copy
   uses.
2. It posts the text to the server.
3. Go's `WriteBatch` adds the "keep watching" footer from section 8 and writes
   the file with the scratch-then-rename handoff.
4. The watcher sees the new file and tells every open tab with an
   `outbox_changed` message.
5. Each tab asks for the document's batches and shows their states.

The text is built in the browser because Copy has to be. On some setups the
clipboard can only be written during the click itself, so Copy cannot wait for
the server to send it text. The text also includes a guide that lives in the
`vantage-md` package, which Go would have to copy. With one builder, Copy and
Send always say the same thing. (I first recommended building it in Go. That
was the wrong call for these two reasons, and the design records the change in its
[Decision Ledger](review-outbox.md#decision-ledger).)

## 11. Follow-ups, and why Send can repeat itself

A reviewer can keep writing comments while the agent works. Each Send while a
batch is claimed writes a **new** batch; nothing is ever added to a batch an
agent already holds. That is ruling D1. The agent answers the batches oldest
first, which the time stamp in the name gives it for free.

Send is switched off while a batch is "Sent" and unclaimed, so a double click
does not queue the same comments twice. But it is switched back on as soon as
the agent claims, because the reviewer is allowed to send follow-ups. That
opening caused the one problem the first trial found, case 8 in the design:

```text
    Reviewer                      Vantage                outbox/                          Agent
0s      |--- Send: C at round 1 ---->|                      |                               |
        |                            |---- write A.md ----->|                               |
1s      |                            |                      |<-- rename to A.md.claimed ----|
        |<-------- Working ----------|                      |                               |
        | Send is still enabled      |                      |                               |
8s      |------- Send again -------->|                      |                               |
        |                            |---- write B.md ----->|                               |
        |                            |                      | B holds C at round 1 too      |
        |                            |                      |                               | answers C, delivers
        |                            |                      |<---- delete A.md.claimed -----|
        |                            |                      |----- the wait exits on B ---->|
        |                            |                      |<-- rename to B.md.claimed ----|
        |                            |                      |                               | C round 1: answered
        |                            |                      |                               | already, so skips it
```

Send includes every comment that is still waiting for the agent. While the agent
works on batch A, comment C is still waiting: it stays that way until the reply
lands. So the second Send put C in batch B as well, at the same round. It
would have been answered twice. The agent noticed only because `watching.md`
now tells it to skip a comment whose id and round it has already answered.

That skip rule is a stopgap on the agent's side. The real fix, planned in build
step 4, is in Vantage: Send leaves out comments that a claimed batch already
holds at that round, so batch B would carry only what is new.

## 12. Noticing that the agent has gone quiet

*Planned, build step 6.* An agent that fails in a way it can see reports it at
once (section 9). An agent that fails silently cannot: its session might have
crashed, or it might be stuck at a permission prompt with nobody at the
keyboard. The batch then stays `x.md.claimed` with nothing happening.

So a claim counts as stalled after **15 minutes without progress**. Progress is
the claim itself, or any delivery to the inbox for that document. A big batch
answered in several deliveries keeps resetting the clock, so it is never
flagged while it is still making progress:

```text
time   10:00       10:06         10:12                         10:27
         |           |             |                             |
       claim     delivery 1    delivery 2       silence          |
         |           |             |                             |
         |<-15 min ->|             |                             |
                     |<-15 min --->|                             |
                                   |<---------- 15 min --------->|
                                                                 |
                                       the label changes to "picked this
                                       up but hasn't answered", with Re-send
```

The time since the last progress is always shown, not only after 15 minutes. A
reviewer who knows the session has gone can act straight away. The 15 minutes
only change the label.

## 13. Two smaller cases

**The agent watches the wrong checkout.** You may have several worktrees of one
repository, as we do now. Vantage writes batches into the checkout it serves.
An agent waiting in a different checkout never wakes:

```text
Vantage serves    ~/github/vantage-review-outbox   writes x.md here
Agent waits in    ~/github/vantage                 looks here: nothing, ever
```

From Vantage's side this looks like an agent that is busy, so the bar shows
"Sent, not claimed yet". To catch it, `watching.md` names the checkout Vantage
serves, and the agent compares it with its own working directory before it
starts.

**Batches get committed.** A batch can sit in the outbox for minutes. If someone
runs a broad `git add` in that time, the reviewer's comments end up in the
history. Vantage writes `.vantage/.gitignore` containing `*`. That ignores
everything under `.vantage/`, the `.gitignore` file included, so adding it
changes nothing `git status` reports.

## 14. Where things stand

| Build step | What | State |
| :--- | :--- | :--- |
| 1 | `watching.md`, `.vantage/.gitignore`, the watcher sees the outbox | Built |
| 2 | Send writes a batch; the panel and bar show the state | Built |
| 3 | The `vantage-watch` skill and its permission list | Not built |
| 4 | Claim-to-rewrite for edited comments (section 7); Send leaves out what a claimed batch holds (section 11) | Not built |
| 5 | Re-send and Dismiss (section 9) | Not built |
| 6 | The 15-minute stalled label (section 12) | Not built |

What is still unknown: whether Claude Code can deliver a wake-up while the
session is busy in the middle of a turn, or only when the turn ends. Either
works. It only changes how long the reviewer waits.
