package summary

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"chatchat/internal/notify"
	"chatchat/internal/store"
)

// Job statuses, as the state endpoint reports them.
const (
	StatusIdle    = "idle"
	StatusPending = "pending"
	StatusFailed  = "failed"
)

// Options tune the runner.
type Options struct {
	// Threshold is the number of new text messages that calls for a
	// summary.
	Threshold int
	// Backoff lists the waits before each retry of a failed model call;
	// its length is the number of retries.
	Backoff []time.Duration
	// BatchMessages and BatchChars bound the messages given to the model in
	// one call. Longer stretches are summarized batch by batch, each batch
	// building on the summary of the one before.
	BatchMessages int
	BatchChars    int
}

// DefaultOptions returns the options used in production.
func DefaultOptions(threshold int) Options {
	return Options{
		Threshold:     threshold,
		Backoff:       []time.Duration{5 * time.Second, 20 * time.Second, time.Minute},
		BatchMessages: 200,
		BatchChars:    60000,
	}
}

// Status is a chat's summary job as the state endpoint reports it.
type Status struct {
	Status string
	// LastFailedAt is when a job last gave up, or 0.
	LastFailedAt int64
}

// job is the summary work of one chat. At most one goroutine runs it.
type job struct {
	running bool
	// recheck asks the goroutine to judge the chat again; leave records
	// that one of the triggers since the last judgement was a leave.
	recheck, leave bool
	cancel         context.CancelFunc

	status       string
	lastFailedAt int64
	// failedThrough is the sequence a job that gave up was summarizing
	// to. The threshold counts again from there, so a failing model is not
	// called on every further message.
	failedThrough int64
}

// Runner writes summaries in the background, one job per chat at a time.
type Runner struct {
	store *store.Store
	hub   *notify.Hub
	model Model
	opts  Options

	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu     sync.Mutex
	jobs   map[string]*job
	closed bool
}

// NewRunner returns a runner. With a nil model it never writes anything
// and every chat stays idle.
func NewRunner(st *store.Store, hub *notify.Hub, model Model, opts Options) *Runner {
	ctx, stop := context.WithCancel(context.Background())
	return &Runner{store: st, hub: hub, model: model, opts: opts, ctx: ctx, stop: stop, jobs: map[string]*job{}}
}

// Close cancels running jobs and waits for them to end.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	r.wg.Wait()
}

// Resume judges every live chat, for work left over from before a restart.
func (r *Runner) Resume(ctx context.Context) error {
	if r.model == nil {
		return nil
	}
	ids, err := r.store.LiveChatIDs(ctx)
	for _, id := range ids {
		r.Trigger(id, false)
	}
	return err
}

// Trigger asks for the chat to be judged after a text message was sent,
// or with leave after a participant left. A running job judges again when
// it finishes.
func (r *Runner) Trigger(chatID string, leave bool) {
	if r.model == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	j := r.jobs[chatID]
	if j == nil {
		j = &job{status: StatusIdle}
		r.jobs[chatID] = j
	}
	j.recheck = true
	j.leave = j.leave || leave
	if j.running {
		return
	}
	j.running = true
	ctx, cancel := context.WithCancel(r.ctx)
	j.cancel = cancel
	r.wg.Add(1)
	go r.run(ctx, chatID, j)
}

// Forget cancels the chat's job and drops its status. Call it once the
// chat is destroyed; the store refuses a late result anyway.
func (r *Runner) Forget(chatID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j := r.jobs[chatID]; j != nil {
		if j.cancel != nil {
			j.cancel()
		}
		delete(r.jobs, chatID)
	}
}

// Status reports the chat's summary job.
func (r *Runner) Status(chatID string) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j := r.jobs[chatID]; j != nil {
		return Status{Status: j.status, LastFailedAt: j.lastFailedAt}
	}
	return Status{Status: StatusIdle}
}

// run judges the chat and writes summaries until no trigger is left.
func (r *Runner) run(ctx context.Context, chatID string, j *job) {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		if !j.recheck || ctx.Err() != nil {
			j.running = false
			j.cancel()
			r.mu.Unlock()
			return
		}
		leave, failedThrough, before := j.leave, j.failedThrough, j.status
		j.recheck, j.leave = false, false
		r.mu.Unlock()

		due, err := r.due(ctx, chatID, leave, failedThrough)
		if err == nil && due {
			r.setStatus(chatID, j, StatusPending, 0)
			var target int64
			target, err = r.generate(ctx, chatID)
			switch {
			case err == nil:
				r.setStatus(chatID, j, StatusIdle, 0)
			case errors.Is(err, store.ErrSummaryConflict):
				// Someone else summarized first; judge the new state.
				r.mu.Lock()
				j.recheck, j.leave = true, j.leave || leave
				r.mu.Unlock()
				r.setStatus(chatID, j, before, 0)
				continue
			case isGone(err) || ctx.Err() != nil:
			default:
				slog.Error("summary failed", "chat_id", chatID, "err", err)
				r.setStatus(chatID, j, StatusFailed, target)
				continue
			}
		}
		switch {
		case isGone(err):
			r.Forget(chatID)
		case err != nil && ctx.Err() == nil:
			slog.Error("summary check failed", "chat_id", chatID, "err", err)
		}
	}
}

func isGone(err error) bool {
	return errors.Is(err, store.ErrChatDestroyed) || errors.Is(err, store.ErrNotFound)
}

// setStatus records the job's status and tells watchers when it changed.
// A failure also records how far the job meant to summarize.
func (r *Runner) setStatus(chatID string, j *job, status string, failedThrough int64) {
	r.mu.Lock()
	changed := j.status != status
	j.status = status
	if status == StatusFailed {
		j.lastFailedAt = r.store.Now().UnixMilli()
		j.failedThrough = failedThrough
		changed = true
	}
	r.mu.Unlock()
	if changed {
		r.hub.Notify(chatID, notify.Summary)
	}
}

// due reports whether the chat needs a new summary: enough new text
// messages, or after a leave any text message the summary does not cover.
func (r *Runner) due(ctx context.Context, chatID string, leave bool, failedThrough int64) (bool, error) {
	c, err := r.store.Chat(ctx, chatID)
	switch {
	case err != nil:
		return false, err
	case c.Destroyed():
		return false, store.ErrChatDestroyed
	}
	latest, err := r.store.LatestSummary(ctx, chatID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if leave {
		n, err := r.store.CountText(ctx, chatID, latest.CoversThrough)
		return n > 0, err
	}
	n, err := r.store.CountText(ctx, chatID, max(latest.CoversThrough, failedThrough))
	return n >= r.opts.Threshold, err
}

// generate summarizes the chat up to its latest message, a batch at a
// time, and returns the sequence it set out to reach. Each batch is
// written on the condition that no other summary came first.
func (r *Runner) generate(ctx context.Context, chatID string) (int64, error) {
	c, err := r.store.Chat(ctx, chatID)
	switch {
	case err != nil:
		return 0, err
	case c.Destroyed():
		return 0, store.ErrChatDestroyed
	}
	target := c.LastSequence
	prev, err := r.store.LatestSummary(ctx, chatID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return target, err
	}
	for prev.CoversThrough < target {
		batch, err := r.batch(ctx, chatID, prev.CoversThrough, target)
		if err != nil {
			return target, err
		}
		if len(batch) == 0 {
			// The messages are gone: the chat was destroyed meanwhile.
			return target, store.ErrChatDestroyed
		}
		content, err := r.summarize(ctx, chatID, Input{Title: c.Title, Goal: c.Goal, Previous: prev.Content, Messages: batch})
		if err != nil {
			return target, err
		}
		sum, err := r.store.AppendSummary(ctx, chatID, prev.Revision, batch[len(batch)-1].Sequence, content, r.model.Name())
		if err != nil {
			return target, err
		}
		r.hub.Notify(chatID, notify.Summary)
		prev = sum
	}
	return target, nil
}

// batch returns the messages in (after, through] that fit one model call.
// A single message always fits.
func (r *Runner) batch(ctx context.Context, chatID string, after, through int64) ([]store.Message, error) {
	msgs, _, err := r.store.Messages(ctx, chatID, after, 0, r.opts.BatchMessages)
	if err != nil {
		return nil, err
	}
	chars := 0
	for i, m := range msgs {
		chars += utf8.RuneCountInString(m.Text)
		if m.Sequence > through || (i > 0 && chars > r.opts.BatchChars) {
			return msgs[:i], nil
		}
	}
	return msgs, nil
}

// summarize calls the model, retrying with backoff.
func (r *Runner) summarize(ctx context.Context, chatID string, in Input) (string, error) {
	for attempt := 0; ; attempt++ {
		out, err := r.model.Summarize(ctx, in)
		if err == nil {
			out, err = Clean(out)
		}
		switch {
		case err == nil:
			return out, nil
		case ctx.Err() != nil:
			return "", ctx.Err()
		case attempt == len(r.opts.Backoff):
			return "", err
		}
		slog.Warn("summary attempt failed", "chat_id", chatID, "attempt", attempt+1, "err", err)
		select {
		case <-time.After(r.opts.Backoff[attempt]):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
