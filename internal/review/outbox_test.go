package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteWatchingGuideWritesGuideAndGitignore(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, WriteWatchingGuide(root))

	guide, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(WatchingRel)))
	require.NoError(t, err)
	require.Contains(t, string(guide), root, "the guide names the checkout Vantage serves")
	require.Contains(t, string(guide), OutboxRel)
	require.NotContains(t, string(guide), servedRootPlaceholder)

	ignore, err := os.ReadFile(filepath.Join(root, ".vantage", ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(ignore), "\n*\n", "everything under .vantage stays out of git, the .gitignore included")
}

func TestWriteWatchingGuideLeavesAnUnchangedGuideAlone(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteWatchingGuide(root))
	path := filepath.Join(root, filepath.FromSlash(WatchingRel))
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, old, old))

	require.NoError(t, WriteWatchingGuide(root))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(old), "an unchanged guide is not rewritten, so a restart does not churn it")
}

func TestWriteWatchingGuideReplacesAStaleGuide(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(WatchingRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("an older version's instructions"), 0o644))

	require.NoError(t, WriteWatchingGuide(root))

	guide, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(guide), "an older version's instructions")
}

func TestWriteWatchingGuideKeepsAnExistingGitignore(t *testing.T) {
	root := t.TempDir()
	ignore := filepath.Join(root, ".vantage", ".gitignore")
	require.NoError(t, os.MkdirAll(filepath.Dir(ignore), 0o755))
	require.NoError(t, os.WriteFile(ignore, []byte("someone's own rules\n"), 0o644))

	require.NoError(t, WriteWatchingGuide(root))

	got, err := os.ReadFile(ignore)
	require.NoError(t, err)
	require.Equal(t, "someone's own rules\n", string(got))
}

func TestRefreshWatchingGuideCreatesNothingInAnUnreviewedRepo(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, RefreshWatchingGuide(root))

	_, err := os.Stat(filepath.Join(root, ".vantage"))
	require.True(t, os.IsNotExist(err), "a repository nobody reviewed gets no .vantage directory")
}

func TestRefreshWatchingGuideWritesWhereVantageStateExists(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(InboxDir(root), 0o755))

	require.NoError(t, RefreshWatchingGuide(root))

	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(WatchingRel)))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, ".vantage", ".gitignore"))
	require.NoError(t, err)
}

func TestEnsureOutboxCreatesTheDirectoryAndGuide(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, EnsureOutbox(root))

	info, err := os.Stat(OutboxDir(root))
	require.NoError(t, err)
	require.True(t, info.IsDir())
	_, err = os.Stat(filepath.Join(root, filepath.FromSlash(WatchingRel)))
	require.NoError(t, err)
}

var batchTime = time.UnixMilli(1762300000123)

func TestWriteBatchWritesACompleteBatchUnderAnOldestFirstName(t *testing.T) {
	root := t.TempDir()

	name, err := WriteBatch(root, "docs/plan.md", "## Review Comments\n", batchTime)
	require.NoError(t, err)

	require.Regexp(t, `^1762300000123-docs__plan\.md-[0-9a-f]{8}\.md$`, name)
	got, err := os.ReadFile(filepath.Join(OutboxDir(root), name))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(got), "## Review Comments\n"))
	require.Contains(t, string(got), "until ls .vantage/outbox/*.md", "every batch ends with the instruction to wait again")
	require.Contains(t, string(got), WatchingRel)
	entries, err := os.ReadDir(OutboxDir(root))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no scratch file is left behind")
	_, err = os.Stat(filepath.Join(root, filepath.FromSlash(WatchingRel)))
	require.NoError(t, err, "the first batch never lands without its instructions")
}

func TestWriteBatchNeverOverwritesABatchSentInTheSameMillisecond(t *testing.T) {
	root := t.TempDir()

	first, err := WriteBatch(root, "a.md", "one", batchTime)
	require.NoError(t, err)
	second, err := WriteBatch(root, "a.md", "two", batchTime)
	require.NoError(t, err)

	require.NotEqual(t, first, second)
	require.Less(t, first, second, "the later batch still sorts after the earlier one")
}

func TestWriteBatchRejectsAnEmptyText(t *testing.T) {
	_, err := WriteBatch(t.TempDir(), "a.md", "  \n", batchTime)
	require.ErrorIs(t, err, ErrEmptyBatch)
}

// a/b.md and a__b.md flatten to the same name, so the stem carries a hash of
// the real path: one document's state must never show on the other.
func TestBatchStemKeepsPathsThatFlattenAlikeApart(t *testing.T) {
	require.NotEqual(t, batchStem("a/b.md"), batchStem("a__b.md"))
	require.Regexp(t, `^[A-Za-z0-9._-]+$`, batchStem("docs/$(rm -rf)/x y.md"))
}

func TestOutboxBatchesReportsEachStateForOneDocument(t *testing.T) {
	root := t.TempDir()
	sent, err := WriteBatch(root, "a.md", "one", batchTime)
	require.NoError(t, err)
	claimed, err := WriteBatch(root, "a.md", "two", batchTime.Add(time.Second))
	require.NoError(t, err)
	failed, err := WriteBatch(root, "a.md", "three", batchTime.Add(2*time.Second))
	require.NoError(t, err)
	_, err = WriteBatch(root, "other.md", "not mine", batchTime)
	require.NoError(t, err)
	dir := OutboxDir(root)
	require.NoError(t, os.Rename(filepath.Join(dir, claimed), filepath.Join(dir, claimed+".claimed")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, failed+".failed.reason"), []byte("a.md was deleted\n"), 0o644))
	require.NoError(t, os.Rename(filepath.Join(dir, failed), filepath.Join(dir, failed+".failed")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1-a.md-zzzz.md.writing"), []byte("scratch"), 0o644))

	got, err := OutboxBatches(root, "a.md")
	require.NoError(t, err)

	require.Equal(t, []Batch{
		{Name: sent, State: BatchSent, SentAt: batchTime.UnixMilli()},
		{Name: claimed, State: BatchClaimed, SentAt: batchTime.Add(time.Second).UnixMilli()},
		{Name: failed, State: BatchFailed, SentAt: batchTime.Add(2 * time.Second).UnixMilli(), Reason: "a.md was deleted"},
	}, got)
}

func TestOutboxBatchesCountsABatchBeingRewrittenAsSent(t *testing.T) {
	root := t.TempDir()
	name, err := WriteBatch(root, "a.md", "one", batchTime)
	require.NoError(t, err)
	dir := OutboxDir(root)
	require.NoError(t, os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".updating")))

	got, err := OutboxBatches(root, "a.md")
	require.NoError(t, err)
	require.Equal(t, []Batch{{Name: name, State: BatchSent, SentAt: batchTime.UnixMilli()}}, got)
}

func TestOutboxBatchesIsEmptyWithoutAnOutbox(t *testing.T) {
	got, err := OutboxBatches(t.TempDir(), "a.md")
	require.NoError(t, err)
	require.Empty(t, got)
}
