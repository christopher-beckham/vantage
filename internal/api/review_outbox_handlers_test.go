package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/vantage/internal/review"
	"github.com/stretchr/testify/require"
)

type outboxReply struct {
	Batches []review.Batch `json:"batches"`
}

func TestReviewOutboxSendWritesABatchAndReturnsTheDocumentsBatches(t *testing.T) {
	e := newTestEnv(t, false)
	writeFile(t, e.dir, "docs/a.md", cmdDoc)

	w := e.do(e.h.ReviewOutboxSend, http.MethodPost, "/review/outbox?path=docs/a.md", `{"text":"## Review Comments for docs/a.md"}`, true)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var got outboxReply
	decode(t, w, &got)
	require.Len(t, got.Batches, 1)
	require.Equal(t, review.BatchSent, got.Batches[0].State)
	body, err := os.ReadFile(filepath.Join(review.OutboxDir(e.dir), got.Batches[0].Name))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(body), "## Review Comments for docs/a.md"))
}

func TestReviewOutboxGetListsOnlyThatDocumentsBatches(t *testing.T) {
	e := newTestEnv(t, false)
	writeFile(t, e.dir, "a.md", cmdDoc)
	writeFile(t, e.dir, "b.md", cmdDoc)
	e.do(e.h.ReviewOutboxSend, http.MethodPost, "/review/outbox?path=a.md", `{"text":"for a"}`, true)
	e.do(e.h.ReviewOutboxSend, http.MethodPost, "/review/outbox?path=b.md", `{"text":"for b"}`, true)

	w := e.do(e.h.ReviewOutboxGet, http.MethodGet, "/review/outbox?path=a.md", "", true)

	require.Equal(t, http.StatusOK, w.Code)
	var got outboxReply
	decode(t, w, &got)
	require.Len(t, got.Batches, 1)
}

func TestReviewOutboxGetIsAnEmptyListBeforeAnySend(t *testing.T) {
	e := newTestEnv(t, false)

	w := e.do(e.h.ReviewOutboxGet, http.MethodGet, "/review/outbox?path=a.md", "", true)

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"batches":[]}`, w.Body.String())
}

func TestReviewOutboxSendRejections(t *testing.T) {
	e := newTestEnv(t, false)
	writeFile(t, e.dir, "a.md", cmdDoc)
	cases := []struct {
		name, target, body string
	}{
		{"no path", "/review/outbox", `{"text":"x"}`},
		{"path outside the repository", "/review/outbox?path=../a.md", `{"text":"x"}`},
		{"absolute path", "/review/outbox?path=/etc/passwd", `{"text":"x"}`},
		{"empty text", "/review/outbox?path=a.md", `{"text":"  "}`},
		{"malformed body", "/review/outbox?path=a.md", `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(e.h.ReviewOutboxSend, http.MethodPost, tc.target, tc.body, true)
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		})
	}
	_, err := os.Stat(review.OutboxDir(e.dir))
	require.True(t, os.IsNotExist(err), "a rejected send writes nothing")
}
