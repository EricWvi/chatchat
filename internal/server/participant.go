package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatchat/internal/auth"
	"chatchat/internal/notify"
	"chatchat/internal/store"
)

// participationHeader carries the participation ID next to its secret.
const participationHeader = "Chatchat-Participation"

// resolver authenticates a participant request and returns the live
// participation it acts as.
type resolver func(http.ResponseWriter, *http.Request) (store.Participation, error)

// participant wraps a participant endpoint: it authenticates the request,
// renews the participant's lease and then runs fn.
func (s *Server) participant(resolve resolver, fn func(http.ResponseWriter, *http.Request, store.Participation) error) http.Handler {
	return s.handle(func(w http.ResponseWriter, r *http.Request) error {
		p, err := resolve(w, r)
		if err != nil {
			return err
		}
		if err := s.touch(r.Context(), p); err != nil {
			return err
		}
		return fn(w, r, p)
	})
}

func (s *Server) touch(ctx context.Context, p store.Participation) error {
	cameOnline, err := s.store.Touch(ctx, p.ID, s.opts.Limits.PresenceLease)
	if cameOnline {
		s.hub.Notify(p.ChatID, notify.Participants)
	}
	return err
}

// cliParticipant authenticates with a participation ID and secret. The
// state of the participation or its chat is only revealed to a caller
// holding the right secret; anyone else gets invalid_credentials.
func (s *Server) cliParticipant(w http.ResponseWriter, r *http.Request) (store.Participation, error) {
	token, present := bearer(r)
	switch {
	case strings.HasPrefix(token, store.UserTokenPrefix):
		return store.Participation{}, errUserTokenOnAgent
	case !present:
		if _, err := r.Cookie(auth.SessionCookie); err == nil {
			return store.Participation{}, wrongCredential("a website session cannot be used on participant endpoints; use the credentials returned by join")
		}
		return store.Participation{}, errInvalidCreds
	}
	p, err := s.store.Participation(r.Context(), r.Header.Get(participationHeader))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return p, errInvalidCreds
	case err != nil:
		return p, err
	case p.Kind != store.KindCLI || !p.SecretMatches(token):
		return p, errInvalidCreds
	case p.ChatID != r.PathValue("chat_id"):
		return p, errChatMismatch
	}
	_, err = s.checkLive(r.Context(), p)
	return p, err
}

// webParticipant authenticates with the login session and acts as the
// user's web participation in the chat.
func (s *Server) webParticipant(w http.ResponseWriter, r *http.Request) (store.Participation, error) {
	u, err := s.sessionUser(w, r)
	if err != nil {
		return store.Participation{}, err
	}
	chatID := r.PathValue("chat_id")
	if c, err := s.store.Chat(r.Context(), chatID); err != nil {
		return store.Participation{}, err
	} else if c.Destroyed() {
		return store.Participation{}, errDestroyed
	}
	p, err := s.store.WebParticipation(r.Context(), chatID, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		return p, errNotEntered
	} else if err != nil {
		return p, err
	}
	_, err = s.checkLive(r.Context(), p)
	return p, err
}

// checkLive fails when the chat was destroyed or the participant left;
// destruction is reported first because it is final.
func (s *Server) checkLive(ctx context.Context, p store.Participation) (store.Chat, error) {
	c, err := s.store.Chat(ctx, p.ChatID)
	switch {
	case err != nil:
		return c, err
	case c.Destroyed():
		return c, errDestroyed
	case p.LeftAt != 0:
		return c, errLeft
	}
	return c, nil
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request, p store.Participation) error {
	ctx := r.Context()
	// Read before the state, so a change in between makes it look stale.
	version := s.hub.Version(p.ChatID)
	ci, err := s.store.ChatInfo(ctx, p.ChatID, s.opts.Limits.PresenceLease)
	if err != nil {
		return err
	}
	ps, err := s.store.Participants(ctx, p.ChatID)
	if err != nil {
		return err
	}
	var sum *summaryView
	if latest, err := s.store.LatestSummary(ctx, p.ChatID); err == nil {
		sum = viewSummary(latest)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	job := s.summary.Status(p.ChatID)
	now := s.store.Now().UnixMilli()
	participants := make([]participantView, len(ps))
	for i, q := range ps {
		participants[i] = s.viewParticipant(q, now)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chat": map[string]any{
			"chat_id": ci.ID, "title": ci.Title, "goal": ci.Goal, "created_by_me": ci.CreatorID == p.UserID,
			"created_at": jsonTime(ci.CreatedAt), "expires_at": jsonTime(ci.ExpiresAt),
			"idle_destroy_at": s.idleDestroyAt(ci), "latest_sequence": ci.LastSequence,
		},
		"summary":       sum,
		"summary_job":   map[string]any{"status": job.Status, "last_failed_at": jsonTime(job.LastFailedAt)},
		"participants":  participants,
		"you":           map[string]string{"participation_id": p.ID},
		"state_version": version,
	})
	return nil
}

// sequenceParam reads a non-negative sequence parameter; ok is false when
// it is absent.
func sequenceParam(r *http.Request, name string) (v int64, ok bool, err error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, false, nil
	}
	v, err = strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, false, errInvalid(name + " must be a non-negative integer")
	}
	return v, true, nil
}

func (s *Server) getMessages(w http.ResponseWriter, r *http.Request, p store.Participation) error {
	after, hasAfter, err := sequenceParam(r, "after_sequence")
	if err != nil {
		return err
	}
	before, hasBefore, err := sequenceParam(r, "before_sequence")
	switch {
	case err != nil:
		return err
	case hasAfter && hasBefore:
		return errInvalid("after_sequence and before_sequence cannot be used together")
	case hasBefore && before == 0:
		return errInvalid("before_sequence must be a positive integer")
	}
	limit, err := s.pageSize(r)
	if err != nil {
		return err
	}
	msgs, more, err := s.store.Messages(r.Context(), p.ChatID, after, before, limit)
	if err != nil {
		return err
	}
	c, err := s.checkLive(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": viewMessages(msgs), "has_more": more, "latest_sequence": c.LastSequence,
	})
	return nil
}

// waitMessages long-polls for messages after after_sequence. A web wait
// also returns early when the summary or the participants change, and
// says what changed; a CLI wait only returns for messages, so an agent is
// not woken by things it does not act on.
func (s *Server) waitMessages(w http.ResponseWriter, r *http.Request, p store.Participation) error {
	ctx := r.Context()
	after, ok, err := sequenceParam(r, "after_sequence")
	if err != nil {
		return err
	}
	if !ok {
		return errInvalid("after_sequence is required")
	}
	limit, err := s.pageSize(r)
	if err != nil {
		return err
	}
	timeout := s.opts.Limits.WaitMax
	if raw := r.URL.Query().Get("timeout_seconds"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return errInvalid("timeout_seconds must be a non-negative integer")
		}
		timeout = min(timeout, time.Duration(n)*time.Second)
	}
	web := p.Kind == store.KindWeb
	// A web client passes the state_version of the state it shows, so
	// changes made while it was not waiting are reported too.
	q := r.URL.Query()
	seen, hasSeen := notify.Version(q.Get("state_version")), q.Has("state_version")

	// Watch before reading, so a change between the read and the wait
	// still wakes us.
	watcher := s.hub.Watch(p.ChatID)
	defer watcher.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var changed notify.Change
	for {
		msgs, more, err := s.store.Messages(ctx, p.ChatID, after, 0, limit)
		if err != nil {
			return err
		}
		// Re-read the participation: it may have left, or the chat may be
		// gone, while we waited.
		cur, err := s.store.Participation(ctx, p.ID)
		if err != nil {
			return err
		}
		c, err := s.checkLive(ctx, cur)
		if err != nil {
			return err
		}
		version := s.hub.Version(p.ChatID)
		if web && hasSeen {
			changed |= s.hub.Diff(seen, version)
		}
		if len(msgs) > 0 {
			changed |= notify.Messages
		}
		if changed == 0 || (!web && len(msgs) == 0) {
			select {
			case <-watcher.C():
				// Messages are judged by the read above, not the signal.
				changed |= watcher.Take() &^ notify.Messages
				continue
			case <-timer.C:
				// Time is up; msgs is empty and so is the answer.
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		resp := map[string]any{"messages": viewMessages(msgs), "has_more": more, "latest_sequence": c.LastSequence}
		if web {
			resp["changed"] = changeNames(changed)
			resp["state_version"] = version
		}
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
}

func changeNames(c notify.Change) []string {
	out := []string{}
	for _, n := range []struct {
		c    notify.Change
		name string
	}{{notify.Messages, "messages"}, {notify.Summary, "summary"}, {notify.Participants, "participants"}} {
		if c&n.c != 0 {
			out = append(out, n.name)
		}
	}
	return out
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, p store.Participation) error {
	var req struct {
		Text            string `json:"text"`
		ClientRequestID string `json:"client_request_id"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	m, created, err := s.store.SendText(r.Context(), p, req.Text, req.ClientRequestID)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		s.hub.Notify(p.ChatID, notify.Messages)
		s.summary.Trigger(p.ChatID, false)
	}
	writeJSON(w, status, viewMessage(m))
	return nil
}

func (s *Server) proposeEnd(w http.ResponseWriter, r *http.Request, p store.Participation) error {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	m, err := s.store.ProposeEnd(r.Context(), p, req.Reason)
	if err != nil {
		return err
	}
	s.hub.Notify(p.ChatID, notify.Messages)
	writeJSON(w, http.StatusCreated, viewMessage(m))
	return nil
}

func (s *Server) leave(w http.ResponseWriter, r *http.Request, p store.Participation) error {
	p, err := s.store.Leave(r.Context(), p.ID)
	if err != nil {
		return err
	}
	s.hub.Notify(p.ChatID, notify.Messages|notify.Participants)
	s.summary.Trigger(p.ChatID, true)
	writeJSON(w, http.StatusOK, map[string]any{"participation_id": p.ID, "left_at": jsonTime(p.LeftAt)})
	return nil
}

// enter finds or creates the user's web participant in the chat.
func (s *Server) enter(w http.ResponseWriter, r *http.Request) error {
	u, err := s.sessionUser(w, r)
	if err != nil {
		return err
	}
	p, entered, err := s.store.Enter(r.Context(), r.PathValue("chat_id"), u)
	if err != nil {
		return err
	}
	change := notify.Participants
	if entered {
		change |= notify.Messages
	}
	s.hub.Notify(p.ChatID, change)
	writeJSON(w, http.StatusOK, s.viewParticipant(p, s.store.Now().UnixMilli()))
	return nil
}

func (s *Server) destroyChat(w http.ResponseWriter, r *http.Request) error {
	u, err := s.sessionUser(w, r)
	if err != nil {
		return err
	}
	c, err := s.store.Chat(r.Context(), r.PathValue("chat_id"))
	switch {
	case err != nil:
		return err
	case c.Destroyed():
		return errDestroyed
	case c.CreatorID != u.ID:
		return errNotCreator
	}
	if _, err := s.store.DestroyChat(r.Context(), c.ID, store.DestroyByCreator); err != nil {
		return err
	}
	s.summary.Forget(c.ID)
	s.hub.Notify(c.ID, notify.Destroyed)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
