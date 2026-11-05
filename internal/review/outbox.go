package review

// The outbox: the half of a review round that goes to the agent. Vantage writes
// one batch file per Send into .vantage/outbox/, and an agent watching that
// directory claims it by renaming it, answers through the inbox, and waits
// again. watching.md, written beside it, is how the agent learns that loop.
// Design: docs/design/review-outbox.md.

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OutboxRel is the repo-relative outbox directory, slash-separated. The
// watcher compares event paths against it; OutboxDir resolves it against a
// root.
const OutboxRel = ".vantage/outbox"

// WatchingRel is the repo-relative path of the instructions a watching agent
// follows. Vantage rewrites it at every start, so the text an agent reads always
// describes the Vantage that is running, not the one that first wrote it.
const WatchingRel = ".vantage/watching.md"

// vantageGitignore keeps everything under .vantage out of the user's
// repository, itself included, so writing it changes nothing git reports. An
// outbox batch can wait for minutes, long enough for a broad `git add` to
// commit a reviewer's comments.
const vantageGitignore = "# Written by Vantage: everything here is local state.\n*\n"

// servedRootPlaceholder marks where the guide names the checkout Vantage
// serves, so an agent in another worktree can tell it is watching the wrong
// outbox.
const servedRootPlaceholder = "{{SERVED_ROOT}}"

//go:embed watching.md
var watchingGuide []byte

// OutboxDir returns the absolute outbox directory for a repository root.
func OutboxDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(OutboxRel))
}

// EnsureOutbox creates the outbox and the guide beside it. Send calls it before
// writing a batch, so the first batch never lands without its instructions.
func EnsureOutbox(root string) error {
	if err := WriteWatchingGuide(root); err != nil {
		return err
	}
	return os.MkdirAll(OutboxDir(root), 0o755)
}

// RefreshWatchingGuide rewrites the guide where Vantage state already exists
// and creates nothing anywhere else, so a repository nobody has reviewed gets
// no .vantage directory just because Vantage served it.
func RefreshWatchingGuide(root string) error {
	if _, err := os.Stat(filepath.Join(root, ".vantage")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return WriteWatchingGuide(root)
}

// WriteWatchingGuide writes the guide for root, and the .gitignore if there is
// none. An existing .gitignore is someone's choice and is left alone. An
// unchanged guide is left untouched, so a restart does not churn its mtime, and
// a changed one is replaced by rename, so an agent reading it never sees half.
func WriteWatchingGuide(root string) error {
	dir := filepath.Join(root, ".vantage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte(vantageGitignore), 0o644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	want := bytes.ReplaceAll(watchingGuide, []byte(servedRootPlaceholder), []byte(root))
	path := filepath.Join(root, filepath.FromSlash(WatchingRel))
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, want) {
		return nil
	}
	tmp := path + ".writing"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrEmptyBatch is returned when Send carries no text: a batch with nothing in
// it would wake the agent for nothing.
var ErrEmptyBatch = errors.New("review: empty outbox batch")

// The name suffixes a batch moves through. A batch is ready under plain ".md";
// every other state appends to that name, so none of them matches the agent's
// `*.md` wait.
const (
	batchSuffix         = ".md"
	batchWritingSuffix  = ".writing"  // Vantage is writing it
	batchUpdatingSuffix = ".updating" // Vantage claimed it to rewrite it
	batchClaimedSuffix  = ".claimed"  // an agent has it
	batchFailedSuffix   = ".failed"   // the agent could not answer it
	batchReasonSuffix   = ".failed.reason"
)

// BatchState is what the reviewer is shown for one batch.
type BatchState string

const (
	// BatchSent: waiting for an agent. A batch Vantage is rewriting counts as
	// sent, because to the reviewer nothing has happened to it yet.
	BatchSent BatchState = "sent"
	// BatchClaimed: an agent renamed it and is working on it.
	BatchClaimed BatchState = "claimed"
	// BatchFailed: the agent gave up on it and said why.
	BatchFailed BatchState = "failed"
)

// Batch is one outbox file for a document, as the review panel shows it.
type Batch struct {
	Name   string     `json:"name"` // the ready name, whatever state it is in
	State  BatchState `json:"state"`
	SentAt int64      `json:"sent_at"` // unix milliseconds, from the name
	Reason string     `json:"reason,omitempty"`
}

// batchFooter ends every batch, so the instruction to keep watching is the last
// thing the agent reads each round and a context summary cannot drop it.
const batchFooter = `
## Keep watching

When you have delivered your replies, delete this batch's ` + "`.claimed`" + ` file,
start this again **in the background**, then end your turn:

` + "```bash\nuntil ls .vantage/outbox/*.md >/dev/null 2>&1; do sleep 1; done\n```" + `

The loop, and what to do when a batch cannot be answered, is in
` + "`" + WatchingRel + "`" + `.
`

var unsafeStemChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// batchStem names a document inside a batch's file name. The flattened path is
// for people reading the directory; the hash is what keeps two paths that
// flatten alike (a/b.md and a__b.md) from sharing state.
func batchStem(docPath string) string {
	flat := strings.ReplaceAll(docPath, "/", "__")
	flat = strings.ReplaceAll(flat, `\`, "__")
	flat = unsafeStemChar.ReplaceAllString(flat, "_")
	sum := sha256.Sum256([]byte(docPath))
	return flat + "-" + hex.EncodeToString(sum[:4])
}

// WriteBatch writes text as a new batch for docPath and returns its name. The
// name starts with the send time in milliseconds, zero-padded, so a plain sort
// is oldest first; a second batch in the same millisecond takes the next free
// one rather than replacing the first. The text is written under a scratch
// name and renamed, so an agent never reads half a batch.
func WriteBatch(root, docPath, text string, now time.Time) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyBatch
	}
	if err := EnsureOutbox(root); err != nil {
		return "", err
	}
	dir := OutboxDir(root)
	stem := batchStem(docPath)
	body := strings.TrimRight(text, "\n") + "\n" + batchFooter
	for ms := now.UnixMilli(); ; ms++ {
		name := fmt.Sprintf("%013d-%s%s", ms, stem, batchSuffix)
		taken, err := batchNameTaken(dir, name)
		if err != nil {
			return "", err
		}
		if taken {
			continue
		}
		tmp := filepath.Join(dir, name+batchWritingSuffix)
		if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
		return name, nil
	}
}

// batchNameTaken reports whether a batch already uses name in any state.
func batchNameTaken(dir, name string) (bool, error) {
	for _, suffix := range []string{"", batchWritingSuffix, batchUpdatingSuffix, batchClaimedSuffix, batchFailedSuffix} {
		if _, err := os.Lstat(filepath.Join(dir, name+suffix)); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

// OutboxBatches lists docPath's batches, oldest first. A scratch file, a reason
// file and another document's batch are skipped; a missing outbox is no
// batches at all.
func OutboxBatches(root, docPath string) ([]Batch, error) {
	dir := OutboxDir(root)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	want := "-" + batchStem(docPath) + batchSuffix
	var out []Batch
	for _, ent := range entries {
		name, state, ok := parseBatchName(ent.Name())
		if !ok || !strings.HasSuffix(name, want) {
			continue
		}
		ms, err := strconv.ParseInt(strings.TrimSuffix(name, want), 10, 64)
		if err != nil {
			continue
		}
		b := Batch{Name: name, State: state, SentAt: ms}
		if state == BatchFailed {
			if reason, err := os.ReadFile(filepath.Join(dir, name+batchReasonSuffix)); err == nil {
				b.Reason = strings.TrimSpace(string(reason))
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parseBatchName splits an outbox file name into its ready name and state.
// Anything that is not a batch in a reader-visible state reports false.
func parseBatchName(file string) (string, BatchState, bool) {
	switch {
	case strings.HasSuffix(file, batchSuffix+batchClaimedSuffix):
		return strings.TrimSuffix(file, batchClaimedSuffix), BatchClaimed, true
	case strings.HasSuffix(file, batchSuffix+batchFailedSuffix):
		return strings.TrimSuffix(file, batchFailedSuffix), BatchFailed, true
	case strings.HasSuffix(file, batchSuffix+batchUpdatingSuffix):
		return strings.TrimSuffix(file, batchUpdatingSuffix), BatchSent, true
	case strings.HasSuffix(file, batchSuffix):
		return file, BatchSent, true
	}
	return "", "", false
}
