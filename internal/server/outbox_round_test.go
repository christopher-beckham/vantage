package server

// Review rounds through the outbox, end to end, with no browser and no real
// agent. The server runs with its watcher, the test plays the reviewer through
// the HTTP API, and fakeAgent plays the agent by following
// internal/review/watching.md to the letter. What this covers is Vantage's half
// of the protocol, and that the protocol holds together: whether a real agent
// follows the guide is a different question these tests cannot answer.
// Design: docs/design/review-outbox.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/mschulkind-oss/vantage/internal/gitenv"
	"github.com/mschulkind-oss/vantage/internal/review"
)

// outboxRig is a running single-repo server and a socket listening to it, as a
// browser tab would.
type outboxRig struct {
	t      *testing.T
	srv    *Server
	root   string
	url    string
	pushes chan string // outbox_changed pushes, as raw JSON
}

func newOutboxRig(t *testing.T) *outboxRig {
	t.Helper()
	srv, root := singleRepoServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	ts := httptest.NewServer(srv.Handler())

	ws, _, err := websocket.Dial(ctx, "ws"+ts.URL[len("http"):]+"/api/ws", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		ts.Close()
		cancel()
		<-done
		_ = srv.Shutdown(context.Background())
	})

	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()
	_, _, err = ws.Read(waitCtx) // the hello
	require.NoError(t, err)
	awaitWatching(waitCtx, t, ws, map[string]string{"": root}, nil)
	require.NoError(t, os.Remove(filepath.Join(root, "probe.md")), "the probe that proved the watcher is up")

	r := &outboxRig{t: t, srv: srv, root: root, url: ts.URL, pushes: make(chan string, 256)}
	go func() {
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "outbox_changed" {
				r.pushes <- string(data)
			}
		}
	}()
	return r
}

// call makes one API request and returns the body, failing on any status but
// 200.
func (r *outboxRig) call(method, target, body string) []byte {
	r.t.Helper()
	req, err := http.NewRequest(method, r.url+"/api"+target, strings.NewReader(body))
	require.NoError(r.t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(r.t, err)
	require.Equal(r.t, http.StatusOK, resp.StatusCode, "%s %s: %s", method, target, data)
	return data
}

// comment adds a comment to doc.md, anchored on its body line, as the
// reviewer does by selecting text and typing.
func (r *outboxRig) comment(id, text string) {
	r.t.Helper()
	r.call(http.MethodPost, "/review/comments?path=doc.md", fmt.Sprintf(
		`{"id":%q,"comment":%q,"anchor":{"source_line":3,"block_text_hash":"x","selection_offset":0,"selection_length":0},"created_at":1717000000}`,
		id, text))
}

// send clicks Send for doc.md with the given comments. The browser builds the
// real text; this one carries the parts an agent acts on, in the same form:
// the document's path and each comment's `[id]` and `round:N`.
func (r *outboxRig) send(ids ...string) {
	r.t.Helper()
	var b strings.Builder
	b.WriteString("## Review Comments for doc.md\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "\n### `[%s]` `round:0`\n\nplease tighten\n", id[:8])
	}
	body, err := json.Marshal(map[string]string{"text": b.String()})
	require.NoError(r.t, err)
	r.call(http.MethodPost, "/review/outbox?path=doc.md", string(body))
}

// batches is what the review panel fetches to show the outbox state.
func (r *outboxRig) batches() []review.Batch {
	r.t.Helper()
	var got struct {
		Batches []review.Batch `json:"batches"`
	}
	require.NoError(r.t, json.Unmarshal(r.call(http.MethodGet, "/review/outbox?path=doc.md", ""), &got))
	return got.Batches
}

// states is batches reduced to their states, oldest first.
func (r *outboxRig) states() []review.BatchState {
	out := []review.BatchState{}
	for _, b := range r.batches() {
		out = append(out, b.State)
	}
	return out
}

// drain forgets the pushes so far, so the next expectPush is about the next
// change only.
func (r *outboxRig) drain() {
	for {
		select {
		case <-r.pushes:
		default:
			return
		}
	}
}

// expectPush waits for an outbox_changed push and checks its shape: a browser
// reloads the outbox on this message alone, so without it the panel shows the
// old state until something else happens.
func (r *outboxRig) expectPush(after string) {
	r.t.Helper()
	select {
	case msg := <-r.pushes:
		require.JSONEq(r.t, `{"type":"outbox_changed","repo":""}`, msg)
	case <-time.After(10 * time.Second):
		r.t.Fatalf("no outbox_changed push after %s", after)
	}
}

// expectStates waits until the panel would show want.
func (r *outboxRig) expectStates(want ...review.BatchState) {
	r.t.Helper()
	if want == nil {
		want = []review.BatchState{}
	}
	var got []review.BatchState
	deadline := time.Now().Add(10 * time.Second)
	for {
		got = r.states()
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("outbox states: got %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// agentReplies is how many agent reactions the comment has.
func (r *outboxRig) agentReplies(id string) int {
	r.t.Helper()
	data, err := r.srv.reviews.Get("doc.md", "")
	require.NoError(r.t, err)
	n := 0
	for _, c := range data.Comments {
		if c.ID != id {
			continue
		}
		for _, re := range c.Reactions {
			if re.Actor == "agent" {
				n++
			}
		}
	}
	return n
}

func (r *outboxRig) expectReplies(id string, want int) {
	r.t.Helper()
	waitFor(r.t, fmt.Sprintf("%d agent replies on %s", want, id), func() bool { return r.agentReplies(id) == want })
}

// fakeAgent follows watching.md, step for step. Each method is one step, so a
// test can stop between steps to look, or run two agents against each other.
type fakeAgent struct {
	t    *testing.T
	root string
	name string
}

var commentHeading = regexp.MustCompile("`\\[([0-9a-f]{8})\\]` `round:(\\d+)`")

// ready lists the batches waiting, oldest first: loop step 1, and the
// question the background wait asks every second.
func (a *fakeAgent) ready() []string {
	a.t.Helper()
	matches, err := filepath.Glob(filepath.Join(review.OutboxDir(a.root), "*.md"))
	require.NoError(a.t, err)
	sort.Strings(matches)
	return matches
}

// wait is the background wait: it returns once a batch is ready.
func (a *fakeAgent) wait() []string {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if m := a.ready(); len(m) > 0 {
			return m
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("%s: no batch arrived", a.name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// claim is loop step 3. It reports false when someone else renamed the batch
// first, which the guide says to treat as "go back to step 1".
func (a *fakeAgent) claim(batch string) (string, bool) {
	claimed := batch + ".claimed"
	err := os.Rename(batch, claimed)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	require.NoError(a.t, err)
	return claimed, true
}

// answer is loop steps 5 and 6: read the claimed batch, edit the document,
// deliver one reply per comment through the inbox, and delete the claim.
func (a *fakeAgent) answer(claimed string) {
	a.t.Helper()
	body, err := os.ReadFile(claimed)
	require.NoError(a.t, err)
	text := string(body)
	require.Contains(a.t, text, "until ls .vantage/outbox/*.md", "every batch ends by telling the agent to wait again")

	doc := filepath.Join(a.root, "doc.md")
	cur, err := os.ReadFile(doc)
	require.NoError(a.t, err)
	require.NoError(a.t, os.WriteFile(doc, append(cur, []byte("tightened by "+a.name+"\n")...), 0o644))

	var lines strings.Builder
	for i, m := range commentHeading.FindAllStringSubmatch(text, -1) {
		fmt.Fprintf(&lines, `{"path":"doc.md","id":%q,"summary":"tightened","nonce":"%s-%s-%d","round":%s}`+"\n",
			m[1], a.name, filepath.Base(claimed), i, m[2])
	}
	require.NotZero(a.t, lines.Len(), "the batch names at least one comment")
	inbox := review.InboxDir(a.root)
	require.NoError(a.t, os.MkdirAll(inbox, 0o755))
	final := filepath.Join(inbox, a.name+"-"+filepath.Base(claimed)+".jsonl")
	require.NoError(a.t, os.WriteFile(final+".writing", []byte(lines.String()), 0o644))
	require.NoError(a.t, os.Rename(final+".writing", final))
}

// finish deletes the claim, which ends the round.
func (a *fakeAgent) finish(claimed string) {
	a.t.Helper()
	require.NoError(a.t, os.Remove(claimed))
}

// fail reports a batch it cannot answer: the reason first, then the rename,
// so Vantage never shows a failure without one.
func (a *fakeAgent) fail(claimed, reason string) {
	a.t.Helper()
	ready := strings.TrimSuffix(claimed, ".claimed")
	require.NoError(a.t, os.WriteFile(ready+".failed.reason", []byte(reason+"\n"), 0o644))
	require.NoError(a.t, os.Rename(claimed, ready+".failed"))
}

func gitStatus(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = root
	cmd.Env = gitenv.Scrubbed()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

// One round as the design draws it in §3, checking at each step what the
// reviewer's panel would show and that the browser was told to look.
func TestOutboxRoundEndToEnd(t *testing.T) {
	r := newOutboxRig(t)
	agent := &fakeAgent{t: t, root: r.root, name: "agent"}
	r.comment("c1a2b3c4deadbeef", "please tighten")

	r.drain()
	r.send("c1a2b3c4deadbeef")
	r.expectPush("Send")
	r.expectStates(review.BatchSent)

	guide, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(review.WatchingRel)))
	require.NoError(t, err, "the first Send writes the guide the batch points to")
	require.Contains(t, string(guide), r.root, "the guide names the checkout Vantage serves")

	batches := agent.wait()
	require.Len(t, batches, 1)
	r.drain()
	claimed, ok := agent.claim(batches[0])
	require.True(t, ok)
	r.expectPush("the claim")
	r.expectStates(review.BatchClaimed)

	agent.answer(claimed)
	r.expectReplies("c1a2b3c4deadbeef", 1)
	r.expectStates(review.BatchClaimed) // still working until the claim is deleted

	r.drain()
	agent.finish(claimed)
	r.expectPush("the agent deleting its claim")
	r.expectStates()

	require.Empty(t, agent.ready(), "a restarted wait has nothing to wake for")
	require.Equal(t, "M doc.md", gitStatus(t, r.root), "nothing under .vantage shows in git")
}

// A batch sent while no agent is waiting is still there for the next wait,
// and several are answered oldest first (§3, D1).
func TestOutboxBatchesWaitForAnAbsentAgentAndAreAnsweredInOrder(t *testing.T) {
	r := newOutboxRig(t)
	r.comment("aaaaaaaa00000001", "first")
	r.comment("bbbbbbbb00000002", "second")
	r.send("aaaaaaaa00000001")
	r.send("bbbbbbbb00000002")
	r.expectStates(review.BatchSent, review.BatchSent)

	agent := &fakeAgent{t: t, root: r.root, name: "agent"}
	ready := agent.wait()
	require.Len(t, ready, 2)
	first, err := os.ReadFile(ready[0])
	require.NoError(t, err)
	require.Contains(t, string(first), "[aaaaaaaa]", "name order is send order")

	for _, b := range ready {
		claimed, ok := agent.claim(b)
		require.True(t, ok)
		agent.answer(claimed)
		agent.finish(claimed)
	}
	r.expectReplies("aaaaaaaa00000001", 1)
	r.expectReplies("bbbbbbbb00000002", 1)
	r.expectStates()
}

// Two sessions watching one repository both wake for every batch, and the
// rename decides which one answers it: never both, never neither (§7 case 6).
func TestOutboxTwoAgentsNeverAnswerTheSameBatch(t *testing.T) {
	r := newOutboxRig(t)
	const n = 12
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%08x%08x", i+1, i+1)
		r.comment(ids[i], "tighten")
		r.send(ids[i])
	}
	r.expectStates(repeat(review.BatchSent, n)...)

	agents := []*fakeAgent{
		{t: t, root: r.root, name: "one"},
		{t: t, root: r.root, name: "two"},
	}
	ready := agents[0].ready()
	require.Len(t, ready, n)

	var mu sync.Mutex
	won := map[string]string{}
	var doubles []string
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, a := range agents {
		wg.Add(1)
		go func(a *fakeAgent) {
			defer wg.Done()
			<-start
			for _, b := range ready {
				if _, ok := a.claim(b); ok {
					mu.Lock()
					if prev, dup := won[b]; dup {
						doubles = append(doubles, fmt.Sprintf("%s and %s both claimed %s", prev, a.name, filepath.Base(b)))
					}
					won[b] = a.name
					mu.Unlock()
				}
			}
		}(a)
	}
	close(start)
	wg.Wait()
	require.Empty(t, doubles)
	require.Len(t, won, n, "every batch has exactly one owner")

	for b, name := range won {
		a := agents[0]
		if name == "two" {
			a = agents[1]
		}
		a.answer(b + ".claimed")
		a.finish(b + ".claimed")
	}
	for _, id := range ids {
		r.expectReplies(id, 1)
	}
	r.expectStates()
}

// A batch the agent cannot answer is reported, with its reason, and does not
// wake the agent again (§7 case 2, D9).
func TestOutboxAFailedBatchShowsItsReasonAndStopsWaking(t *testing.T) {
	r := newOutboxRig(t)
	agent := &fakeAgent{t: t, root: r.root, name: "agent"}
	r.comment("c1a2b3c4deadbeef", "please tighten")
	r.send("c1a2b3c4deadbeef")

	require.NoError(t, os.Remove(filepath.Join(r.root, "doc.md")))
	claimed, ok := agent.claim(agent.wait()[0])
	require.True(t, ok)
	r.drain()
	agent.fail(claimed, "doc.md was deleted before I could answer.")
	r.expectPush("the failure")

	r.expectStates(review.BatchFailed)
	got := r.batches()
	require.Equal(t, "doc.md was deleted before I could answer.", got[0].Reason)
	require.Empty(t, agent.ready(), "a failed batch does not match the wait")
	require.Zero(t, r.agentReplies("c1a2b3c4deadbeef"))
}

// Where Vantage state exists, a restart rewrites the guide, so an agent that
// starts watching later reads the running version, not the one that first
// wrote it (§5).
func TestOutboxGuideIsRewrittenWhenTheServerStarts(t *testing.T) {
	srv, root := singleRepoServer(t)
	guide := filepath.Join(root, filepath.FromSlash(review.WatchingRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(guide), 0o755))
	require.NoError(t, os.WriteFile(guide, []byte("an old version's guide\n"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	defer func() {
		cancel()
		<-done
		_ = srv.Shutdown(context.Background())
	}()

	waitFor(t, "the guide to be rewritten", func() bool {
		data, err := os.ReadFile(guide)
		return err == nil && strings.Contains(string(data), root)
	})
	require.Equal(t, "", gitStatus(t, root), "the guide and its .gitignore show nothing in git")
}

func repeat[T any](v T, n int) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = v
	}
	return out
}
