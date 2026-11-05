# Watching for Vantage reviews

Vantage writes this file and rewrites it whenever it starts, so it describes the
version of Vantage that is running now. Read it again whenever you start
watching, even if you have before.

When a reviewer clicks **Send** in Vantage, the comments they wrote on one
document land in `.vantage/outbox/` as a batch file ending in `.md`. Watching
means waiting for one in the background, answering it, and waiting again.

## Before you start

**Vantage serves this checkout:**

    {{SERVED_ROOT}}

Watch the outbox there, and edit the documents there. If your working directory
is a different checkout or worktree, say so and stop: a batch in this one never
reaches you, and an edit in yours never reaches the reviewer.

Run one watching session per repository. If two watch, either may answer a
batch, including one that has never seen the document.

## The loop

1. **Answer what is already waiting.** List `.vantage/outbox/*.md` and handle
   each batch in name order, oldest first, before you wait. Names start with a
   time stamp, so a plain sort is oldest first.
2. **Wait in the background.** Run this as a background command, then end your
   turn:

       until ls .vantage/outbox 2>/dev/null | grep -q '\.md$'; do sleep 1; done

   It exits when a batch arrives, and that wakes you.
3. **Claim the batch before you read it.** Rename `NAME.md` to
   `NAME.md.claimed`.
   - If the rename fails because the file is not there, another session or
     Vantage took it. Go back to step 1.
   - If it fails for any other reason, stop watching and tell the user why. The
     file would still be there, so a new wait would exit at once, every time.
4. **Switch over cleanly.** If you were in the middle of other work, finish or
   park the current step first, and say in the conversation that you are
   handling a review.
5. **Answer it.** Read `NAME.md.claimed` and do what it says. Change only the
   reviewed document, with targeted edits, never by rewriting the whole file. If
   an edit is refused because the file changed on disk, read it again and redo
   the edit. If a comment's heading shows the same `[id]` and
   `round:N` as one you have already answered, skip it: a second reply reads
   as a second answer. Check the document and deliver your replies as the batch
   describes.
6. **Clean up.** Delete `NAME.md.claimed`. Keep the review's edits out of any
   commit you make for other work.
7. **Wait again.** Go back to step 1. Each batch ends with the same reminder.

## When a batch cannot be answered

The document may have been moved or deleted, the batch may be unreadable, or
you may be unable to edit the file. Then:

1. Write one or two sentences saying why to `NAME.md.failed.reason`.
2. Rename `NAME.md.claimed` to `NAME.md.failed`, in that order, so Vantage never
   shows a failure without its reason.
3. Say so in the conversation, then go back to step 1 of the loop.

A comment you disagree with is not a failure. Answer it with a reply that says
why.

## Permissions

A round runs these without anyone at the terminal, so allow them ahead of time:

- the background wait above
- renaming and deleting files under `.vantage/outbox/`
- writing files under `.vantage/inbox/`
- editing the repository's Markdown documents
- `uvx vantage-check`
