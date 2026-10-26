package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"chatchat/internal/summary"
)

// fakeModel writes summaries whose decisions section lists every text
// message it has seen as "- #<sequence> <speaker>: <text>", carried over
// from the previous summary. It can be held, or made to fail.
type fakeModel struct {
	mu        sync.Mutex
	calls     []summary.Input
	active    map[string]int // calls in progress per chat title
	overlap   bool           // two calls ran for one chat at once
	failing   bool
	gate      chan struct{} // when set, each call waits for a value
	delay     time.Duration
	cancelled int
}

func (f *fakeModel) Name() string { return "fake-model" }

func (f *fakeModel) Summarize(ctx context.Context, in summary.Input) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	if f.active == nil {
		f.active = map[string]int{}
	}
	f.active[in.Title]++
	if f.active[in.Title] > 1 {
		f.overlap = true
	}
	gate, failing, delay := f.gate, f.failing, f.delay
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active[in.Title]--
		f.mu.Unlock()
	}()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			f.mu.Lock()
			f.cancelled++
			f.mu.Unlock()
			return "", ctx.Err()
		}
	}
	time.Sleep(delay)
	if failing {
		return "", errors.New("model unavailable")
	}
	facts := decisions(in.Previous)
	for _, m := range in.Messages {
		if m.Kind == "text" {
			facts = append(facts, fmt.Sprintf("- #%d %s: %s", m.Sequence, summary.Speaker(m), m.Text))
		}
	}
	goal := in.Goal
	if goal == "" {
		goal = "无"
	}
	return "## 沟通目标\n" + goal + "\n\n## 已确认的事实和决定\n" + strings.Join(facts, "\n") +
		"\n\n## 当前分歧\n无\n\n## 未解决的问题\n无\n\n## 下一步及负责人\n无\n", nil
}

func (f *fakeModel) set(fn func(*fakeModel)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// decisions returns the fact lines of a fake summary.
func decisions(content string) []string {
	var out []string
	for line := range strings.Lines(content) {
		if strings.HasPrefix(line, "- #") {
			out = append(out, strings.TrimSuffix(line, "\n"))
		}
	}
	return out
}

// summaryHarness runs summaries with the fake model, a threshold of
// three text messages and quick retries.
func summaryHarness(t *testing.T, model *fakeModel, tune ...func(*summary.Options)) *harness {
	t.Helper()
	opts := summary.Options{
		Threshold:     3,
		Backoff:       []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
		BatchMessages: 200,
		BatchChars:    60000,
	}
	for _, fn := range tune {
		fn(&opts)
	}
	return newHarnessWith(t, t.TempDir(), Options{Model: model, Summary: opts})
}

type stateSummary struct {
	Summary *struct {
		Revision      int64  `json:"revision"`
		CoversThrough int64  `json:"covers_through_sequence"`
		Content       string `json:"content"`
		Model         string `json:"model"`
		CreatedAt     string `json:"created_at"`
	} `json:"summary"`
	Job struct {
		Status       string  `json:"status"`
		LastFailedAt *string `json:"last_failed_at"`
	} `json:"summary_job"`
	Chat struct {
		LatestSequence int64 `json:"latest_sequence"`
	} `json:"chat"`
}

func (h *harness) summaryState(c cred) stateSummary {
	h.t.Helper()
	r := h.agent(c, "GET", "/state", "")
	h.expect(r, http.StatusOK, "")
	var s stateSummary
	if err := json.Unmarshal(r.Body.Bytes(), &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// eventually polls cond until it holds or five seconds pass.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// summarized waits until the chat's summary reaches revision rev and is
// idle again.
func (h *harness) summarized(c cred, rev int64) stateSummary {
	h.t.Helper()
	var s stateSummary
	h.eventually(fmt.Sprintf("summary revision %d", rev), func() bool {
		s = h.summaryState(c)
		return s.Summary != nil && s.Summary.Revision >= rev && s.Job.Status == "idle"
	})
	return s
}

func TestSummaryAfterThreshold(t *testing.T) {
	model := &fakeModel{}
	h := summaryHarness(t, model)
	alice := h.account("alice")
	r := h.do("POST", "/api/v1/chats", `{"title":"重构","goal":"定方案"}`, withSession(alice))
	h.expect(r, http.StatusCreated, "")
	chat := r.JSON()["chat_id"].(string)
	c := h.join(alice, chat, "Claude") // sequence 1

	s := h.summaryState(c)
	if s.Summary != nil || s.Job.Status != "idle" || s.Job.LastFailedAt != nil {
		t.Fatalf("fresh state = %+v", s)
	}
	// System messages do not count towards the threshold.
	h.agent(c, "POST", "/propose-end", "")
	h.send(c, "one", "")
	h.send(c, "two", "")
	time.Sleep(50 * time.Millisecond)
	if n := model.callCount(); n != 0 {
		t.Fatalf("summarized after two text messages (%d calls)", n)
	}
	h.send(c, "three", "") // sequence 5
	s = h.summarized(c, 1)
	if s.Summary.CoversThrough != 5 || s.Summary.Model != "fake-model" || s.Summary.CreatedAt == "" ||
		!strings.HasPrefix(s.Summary.Content, "## 沟通目标\n定方案") || len(decisions(s.Summary.Content)) != 3 {
		t.Fatalf("summary = %+v", s.Summary)
	}
	// The model saw the system message as context.
	if in := model.calls[0]; len(in.Messages) != 5 || in.Messages[1].Kind != "system" || in.Previous != "" {
		t.Fatalf("input = %+v", in)
	}

	// The next summary builds on this one and starts after it.
	for _, text := range []string{"four", "five", "six"} {
		h.send(c, text, "")
	}
	s = h.summarized(c, 2)
	in := model.calls[1]
	if s.Summary.CoversThrough != 8 || in.Messages[0].Sequence != 6 || !strings.Contains(in.Previous, "three") ||
		len(decisions(s.Summary.Content)) != 6 {
		t.Fatalf("second summary = %+v from %+v", s.Summary, in)
	}
	// A retried send is not a new message and triggers nothing.
	h.send(c, "seven", "rid")
	h.send(c, "seven", "rid")
	time.Sleep(50 * time.Millisecond)
	if model.callCount() != 2 {
		t.Fatalf("calls = %d", model.callCount())
	}
}

func TestSummaryOnLeave(t *testing.T) {
	model := &fakeModel{}
	h := summaryHarness(t, model)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	a := h.join(alice, chat, "A")
	b := h.join(alice, chat, "B")

	// Leaving with nothing but system messages uncovered writes nothing.
	h.expect(h.agent(b, "POST", "/leave", ""), http.StatusOK, "")
	time.Sleep(50 * time.Millisecond)
	if model.callCount() != 0 {
		t.Fatal("summarized system messages only")
	}

	b = h.join(alice, chat, "B")
	h.send(b, "我先走了，结论是用 SQLite", "")
	h.expect(h.agent(b, "POST", "/leave", ""), http.StatusOK, "") // sequence 6
	s := h.summarized(a, 1)
	if s.Summary.CoversThrough != 6 || len(decisions(s.Summary.Content)) != 1 {
		t.Fatalf("summary = %+v", s.Summary)
	}

	// A web participant leaving triggers too.
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	h.expect(h.web(alice, chat, "POST", "/messages", `{"text":"收到"}`), http.StatusCreated, "")
	h.expect(h.web(alice, chat, "POST", "/leave", ""), http.StatusOK, "")
	if s := h.summarized(a, 2); s.Summary.CoversThrough != 9 {
		t.Fatalf("summary = %+v", s.Summary)
	}
}

func TestSummaryInBatches(t *testing.T) {
	model := &fakeModel{}
	h := summaryHarness(t, model, func(o *summary.Options) {
		o.Threshold = 7
		o.BatchMessages = 3
		o.BatchChars = 10
	})
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "A") // sequence 1
	h.send(c, strings.Repeat("长", 12), "")
	for i := range 6 {
		h.send(c, fmt.Sprint(i), "") // sequences 3-8
	}
	// Batches: [1] (the join takes 6 characters), [2] (over the limit,
	// but a batch always takes one message), [3, 4, 5], [6, 7, 8].
	s := h.summarized(c, 4)
	if s.Summary.CoversThrough != 8 || len(decisions(s.Summary.Content)) != 7 {
		t.Fatalf("summary = %+v", s.Summary)
	}
	var firsts []int64
	for _, in := range model.calls {
		firsts = append(firsts, in.Messages[0].Sequence)
	}
	if fmt.Sprint(firsts) != "[1 2 3 6]" {
		t.Fatalf("batches start at %v", firsts)
	}
}

func TestSummaryFailure(t *testing.T) {
	model := &fakeModel{failing: true}
	h := summaryHarness(t, model)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "A")
	for i := range 3 {
		h.send(c, fmt.Sprint(i), "")
	}
	var s stateSummary
	h.eventually("failed status", func() bool {
		s = h.summaryState(c)
		return s.Job.Status == "failed"
	})
	// One call and three retries; no model error reaches the response.
	if s.Summary != nil || s.Job.LastFailedAt == nil || model.callCount() != 4 {
		t.Fatalf("state = %+v after %d calls", s, model.callCount())
	}
	r := h.agent(c, "GET", "/state", "")
	if strings.Contains(r.Body.String(), "unavailable") {
		t.Fatalf("state leaks the model error: %s", r.Body)
	}
	// Messages are unaffected.
	h.expect(h.agent(c, "GET", "/messages?after_sequence=0", ""), http.StatusOK, "")

	// The next message alone does not retry: the threshold counts again
	// from what the failed job covered.
	h.send(c, "3", "")
	time.Sleep(50 * time.Millisecond)
	if model.callCount() != 4 {
		t.Fatalf("retried on the next message: %d calls", model.callCount())
	}
	// Once the model recovers, the next trigger catches up on everything.
	model.set(func(f *fakeModel) { f.failing = false })
	h.send(c, "4", "")
	h.send(c, "5", "")
	s = h.summarized(c, 1)
	if s.Summary.CoversThrough != 7 || len(decisions(s.Summary.Content)) != 6 || s.Job.LastFailedAt == nil {
		t.Fatalf("recovered = %+v", s)
	}
}

func TestDestroyCancelsSummary(t *testing.T) {
	model := &fakeModel{gate: make(chan struct{})}
	h := summaryHarness(t, model)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	c := h.join(alice, chat, "A")
	for i := range 3 {
		h.send(c, fmt.Sprint(i), "")
	}
	h.eventually("pending job", func() bool { return h.summaryState(c).Job.Status == "pending" })

	h.expect(h.web(alice, chat, "DELETE", "", ""), http.StatusNoContent, "")
	h.eventually("cancelled call", func() bool {
		model.mu.Lock()
		defer model.mu.Unlock()
		return model.cancelled == 1
	})
	if _, err := h.store.LatestSummary(t.Context(), chat); err == nil {
		t.Fatal("a summary survived destruction")
	}
	if st := h.srv.summary.Status(chat); st.Status != "idle" {
		t.Fatalf("status kept after destruction: %+v", st)
	}
}

func TestWebWaitReportsSummary(t *testing.T) {
	model := &fakeModel{gate: make(chan struct{})}
	h := summaryHarness(t, model)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "") // sequence 1
	c := h.join(alice, chat, "A")                                         // sequence 2
	for i := range 3 {
		h.send(c, fmt.Sprint(i), "") // sequences 3-5
	}

	wait := func() map[string]any {
		t.Helper()
		r := h.web(alice, chat, "GET", "/messages/wait?after_sequence=5&timeout_seconds=5", "")
		h.expect(r, http.StatusOK, "")
		return r.JSON()
	}
	// The job going pending is a change; so is the summary landing.
	h.eventually("pending job", func() bool { return h.srv.summary.Status(chat).Status == "pending" })
	done := make(chan map[string]any, 1)
	go func() { done <- wait() }()
	time.Sleep(50 * time.Millisecond)
	model.gate <- struct{}{}
	select {
	case got := <-done:
		if fmt.Sprint(got["changed"]) != "[summary]" || len(got["messages"].([]any)) != 0 {
			t.Fatalf("wait = %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	// A CLI wait is not woken by summaries.
	for _, text := range []string{"a", "b", "c"} {
		h.send(c, text, "") // sequences 6-8
	}
	h.eventually("pending job", func() bool { return h.srv.summary.Status(chat).Status == "pending" })
	start := time.Now()
	go func() { done <- h.agent(c, "GET", "/messages/wait?after_sequence=8&timeout_seconds=1", "").JSON() }()
	time.Sleep(50 * time.Millisecond)
	model.gate <- struct{}{}
	if got := <-done; len(got["messages"].([]any)) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("CLI wait = %v after %v", got, time.Since(start))
	}
	if s := h.summarized(c, 2); s.Summary.CoversThrough != 8 {
		t.Fatalf("summary = %+v", s.Summary)
	}
}
