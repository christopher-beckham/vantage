package api

import (
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mschulkind-oss/vantage/internal/review"
)

// The outbox endpoints (docs/design/review-outbox.md): Send writes the text the
// browser assembled as a batch for a watching agent, and GET reports what has
// become of each batch for one document. The browser builds the text with the
// same code Copy uses, so the server only handles files. The watcher sees every
// rename an agent makes and pushes outbox_changed, which is what tells the
// browser to fetch the state again.

// maxBatchBytes bounds one Send. A batch is a document's pending comments with
// their quoted context, so this is a runaway guard, not a limit anyone meets.
const maxBatchBytes = 1 << 20

// outboxResponse is both endpoints' reply. Batches is never null, so the
// browser can render an empty list without a special case.
type outboxResponse struct {
	Batches []review.Batch `json:"batches"`
}

// outboxTarget resolves the repository root and a document path that names a
// file inside it. The path only names the batch, but a non-local one could
// still be made to look like another repository's document.
func (h *Handlers) outboxTarget(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	svc, path, ok := h.reviewCommandTarget(w, r)
	if !ok {
		return "", "", false
	}
	if !filepath.IsLocal(filepath.FromSlash(path)) {
		writeError(w, http.StatusBadRequest, "path must name a file inside the repository")
		return "", "", false
	}
	if svc.FS == nil {
		writeError(w, http.StatusBadRequest, "repository has no filesystem")
		return "", "", false
	}
	return svc.FS.RootPath(), path, true
}

// writeOutbox replies with path's batches.
func writeOutbox(w http.ResponseWriter, root, path string) {
	batches, err := review.OutboxBatches(root, path)
	if err != nil {
		slog.Error("api: reading the review outbox failed", "path", path, "error", err)
		writeError(w, http.StatusInternalServerError, "Failed to read the outbox")
		return
	}
	if batches == nil {
		batches = []review.Batch{}
	}
	writeJSON(w, http.StatusOK, outboxResponse{Batches: batches})
}

// ReviewOutboxGet handles GET /review/outbox?path=…: the document's batches,
// oldest first, each with its state.
func (h *Handlers) ReviewOutboxGet(w http.ResponseWriter, r *http.Request) {
	root, path, ok := h.outboxTarget(w, r)
	if !ok {
		return
	}
	writeOutbox(w, root, path)
}

// ReviewOutboxSend handles POST /review/outbox?path=… with {"text": …}: it
// writes the text as a new batch for the document and replies with all of the
// document's batches.
func (h *Handlers) ReviewOutboxSend(w http.ResponseWriter, r *http.Request) {
	root, path, ok := h.outboxTarget(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBytes)
	var req struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if _, err := review.WriteBatch(root, path, req.Text, time.Now()); err != nil {
		if errors.Is(err, review.ErrEmptyBatch) {
			writeError(w, http.StatusBadRequest, "Nothing to send")
			return
		}
		slog.Error("api: writing a review batch failed", "path", path, "error", err)
		writeError(w, http.StatusInternalServerError, "Failed to write the batch")
		return
	}
	writeOutbox(w, root, path)
}
