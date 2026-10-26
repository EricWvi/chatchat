package server

import (
	"net/http"
	"testing"
	"time"
)

// status returns the participant's status as the state endpoint reports it.
func (h *harness) status(viewer cred, participationID string) string {
	h.t.Helper()
	r := h.agent(viewer, "GET", "/state", "")
	h.expect(r, http.StatusOK, "")
	for _, p := range r.JSON()["participants"].([]any) {
		p := p.(map[string]any)
		if p["participation_id"] == participationID {
			return p["status"].(string)
		}
	}
	h.t.Fatalf("participant %s not in %s", participationID, r.Body)
	return ""
}

func TestPresenceLease(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	a := h.join(alice, chat, "A")
	b := h.join(alice, chat, "B")

	h.clock.Advance(89 * time.Second)
	if s := h.status(b, a.ParticipationID); s != "online" {
		t.Fatalf("a at 89s = %s", s)
	}
	h.clock.Advance(2 * time.Second)
	if s := h.status(b, a.ParticipationID); s != "offline" {
		t.Fatalf("a at 91s = %s", s)
	}
	// Offline credentials still work, and any request renews the lease.
	h.expect(h.agent(a, "GET", "/messages", ""), http.StatusOK, "")
	if s := h.status(b, a.ParticipationID); s != "online" {
		t.Fatalf("a after reading = %s", s)
	}
	h.expect(h.agent(a, "POST", "/leave", ""), http.StatusOK, "")
	if s := h.status(b, a.ParticipationID); s != "left" {
		t.Fatalf("a after leaving = %s", s)
	}
}

func TestIdleChatIsDestroyed(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude")

	// The idle clock starts when the last lease runs out: 90s + 30m.
	h.clock.Advance(90*time.Second + 30*time.Minute - time.Second)
	h.srv.Sweep(t.Context())
	info := h.do("GET", "/api/v1/chats/"+chat, "", withSession(alice)).JSON()
	if info["destroyed_at"] != nil || info["idle_destroy_at"] != "2026-10-26T08:31:30Z" {
		t.Fatalf("before = %v", info)
	}

	// A pending wait learns of the destruction at once.
	done := make(chan response, 1)
	go func() { done <- h.agent(c, "GET", "/messages/wait?after_sequence=1&timeout_seconds=10", "") }()
	time.Sleep(50 * time.Millisecond)
	// The wait renewed the lease, so the chat is no longer idle.
	h.clock.Advance(30*time.Minute + 89*time.Second)
	h.srv.Sweep(t.Context())
	if info := h.do("GET", "/api/v1/chats/"+chat, "", withSession(alice)).JSON(); info["destroyed_at"] != nil {
		t.Fatalf("destroyed while waiting: %v", info)
	}
	h.clock.Advance(2 * time.Second)
	h.srv.Sweep(t.Context())
	select {
	case r := <-done:
		h.expect(r, http.StatusGone, "chat_destroyed")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	info = h.do("GET", "/api/v1/chats/"+chat, "", withSession(alice)).JSON()
	if info["destroy_reason"] != "idle" {
		t.Fatalf("after = %v", info)
	}
	h.expect(h.agent(c, "GET", "/state", ""), http.StatusGone, "chat_destroyed")
	if ms := h.messages(chat); len(ms) != 0 {
		t.Fatalf("messages survived: %v", ms)
	}
}

func TestUnenteredChatIdlesFromCreation(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	h.clock.Advance(30*time.Minute - time.Second)
	h.srv.Sweep(t.Context())
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")

	other := h.createChat(alice, "nobody came")
	h.clock.Advance(30 * time.Minute)
	h.srv.Sweep(t.Context())
	if r := h.do("GET", "/api/v1/chats/"+other, "", withSession(alice)).JSON(); r["destroy_reason"] != "idle" {
		t.Fatalf("other = %v", r)
	}
	// The first chat had alice until her lease lapsed 90s after entering.
	if r := h.do("GET", "/api/v1/chats/"+chat, "", withSession(alice)).JSON(); r["destroyed_at"] != nil {
		t.Fatalf("chat = %v", r)
	}
}

func TestLeavingStartsTheIdleClock(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude")
	h.expect(h.agent(c, "POST", "/leave", ""), http.StatusOK, "")
	// Leaving ends presence at once, not when the lease would have run out.
	h.clock.Advance(30 * time.Minute)
	h.srv.Sweep(t.Context())
	if r := h.do("GET", "/api/v1/chats/"+chat, "", withSession(alice)).JSON(); r["destroy_reason"] != "idle" {
		t.Fatalf("chat = %v", r)
	}
}

func TestChatExpiresEvenWhenInUse(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude")
	for range 24 * 60 {
		h.clock.Advance(time.Minute)
		h.expect(h.agent(c, "GET", "/state", ""), http.StatusOK, "")
		if h.clock.Now().Minute()%10 == 0 {
			h.srv.Sweep(t.Context())
		}
		if r := h.agent(c, "GET", "/state", ""); r.ResponseRecorder.Code != http.StatusOK {
			h.expect(r, http.StatusGone, "chat_destroyed")
			break
		}
	}
	if got := h.clock.Now().Sub(time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)); got != 24*time.Hour {
		t.Fatalf("destroyed after %v", got)
	}
	if r := h.do("GET", "/api/v1/chats/"+chat, "", withSession(alice)).JSON(); r["destroy_reason"] != "expired" {
		t.Fatalf("chat = %v", r)
	}
}
