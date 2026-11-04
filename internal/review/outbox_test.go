package review

import (
	"os"
	"path/filepath"
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
