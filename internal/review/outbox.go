package review

// The outbox: the half of a review round that goes to the agent. Vantage writes
// one batch file per Send into .vantage/outbox/, and an agent watching that
// directory claims it by renaming it, answers through the inbox, and waits
// again. watching.md, written beside it, is how the agent learns that loop.
// Design: docs/design/review-outbox.md.

import (
	"bytes"
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
