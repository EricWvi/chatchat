package server

// Tests for the checks that the requirements (section 9) demand of the
// server. Items 2, 7 and 8 belong to the CLI and the summary phase.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 1. Joining the same chat twice with the same user token gives two
// distinct participants.
func TestJoinTwiceGivesDistinctParticipants(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "重构讨论")

	a := h.join(alice, chat, "Claude")
	b := h.join(alice, chat, "Claude")
	if a.ParticipationID == b.ParticipationID || a.Secret == b.Secret {
		t.Fatalf("second join reused the identity: %+v %+v", a, b)
	}
	for _, c := range []cred{a, b} {
		if !strings.HasPrefix(c.Secret, "ccp_") || !strings.HasPrefix(c.ParticipationID, "p_") {
			t.Fatalf("credential = %+v", c)
		}
	}
	h.expect(h.send(a, "from a", ""), http.StatusCreated, "")
	h.expect(h.send(b, "from b", ""), http.StatusCreated, "")

	// Both are listed separately, and messages are attributed to each.
	state := h.agent(a, "GET", "/state", "")
	h.expect(state, http.StatusOK, "")
	var s struct {
		Participants []struct {
			ParticipationID string `json:"participation_id"`
			Kind            string `json:"kind"`
			Status          string `json:"status"`
		} `json:"participants"`
		You struct {
			ParticipationID string `json:"participation_id"`
		} `json:"you"`
	}
	json.Unmarshal(state.Body.Bytes(), &s)
	if len(s.Participants) != 2 || s.You.ParticipationID != a.ParticipationID {
		t.Fatalf("state = %s", state.Body)
	}
	for _, p := range s.Participants {
		if p.Kind != "cli" || p.Status != "online" {
			t.Fatalf("participant = %+v", p)
		}
	}
	senders := map[string]string{}
	for _, m := range h.messages(chat) {
		if m.Kind == "text" {
			senders[m.Text] = m.ParticipationID
		}
	}
	if senders["from a"] != a.ParticipationID || senders["from b"] != b.ParticipationID {
		t.Fatalf("senders = %v", senders)
	}
}

// 3. A user token cannot act as any participant, and no credential works
// on another group's endpoints.
func TestCredentialGroupsDoNotMix(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")

	// The user token with a real participation ID does not let anyone
	// speak as that participant.
	asToken := h.do("POST", "/api/v1/agent/chats/"+chat+"/messages", `{"text":"hi"}`,
		withToken(alice), func(r *http.Request) { r.Header.Set(participationHeader, c.ParticipationID) })
	h.expect(asToken, http.StatusUnauthorized, "wrong_credential_type")
	for _, sub := range []string{"/state", "/messages?after_sequence=0"} {
		r := h.do("GET", "/api/v1/agent/chats/"+chat+sub, "", withToken(alice),
			func(r *http.Request) { r.Header.Set(participationHeader, c.ParticipationID) })
		h.expect(r, http.StatusUnauthorized, "wrong_credential_type")
	}
	// The login session does not work on CLI endpoints either.
	h.expect(h.do("GET", "/api/v1/agent/chats/"+chat+"/state", "", withSession(alice)),
		http.StatusUnauthorized, "wrong_credential_type")
	// Participation secrets do not work on web or account endpoints.
	h.expect(h.do("GET", "/api/v1/web/chats/"+chat+"/state", "", withCred(c)),
		http.StatusUnauthorized, "wrong_credential_type")
	h.expect(h.do("GET", "/api/v1/me", "", withCred(c)), http.StatusUnauthorized, "wrong_credential_type")
	// User tokens do not work on web endpoints, nor manage tokens.
	h.expect(h.do("GET", "/api/v1/web/chats/"+chat+"/state", "", withToken(alice)),
		http.StatusUnauthorized, "wrong_credential_type")
	h.expect(h.do("GET", "/api/v1/tokens", "", withToken(alice)), http.StatusUnauthorized, "wrong_credential_type")
	// Joining as a CLI participant needs the user token, not the session.
	h.expect(h.do("POST", "/api/v1/chats/"+chat+"/participations", `{"display_name":"x"}`, withSession(alice)),
		http.StatusUnauthorized, "wrong_credential_type")

	if got := h.textMessages(chat); len(got) != 0 {
		t.Fatalf("messages were created: %v", got)
	}
}

// 4. Wrong, unknown or ended credentials cannot act; only the holder of
// the right secret learns why.
func TestInvalidCredentialsAreRejected(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	webState := h.web(alice, chat, "GET", "/state", "").JSON()
	webID := webState["you"].(map[string]any)["participation_id"].(string)

	wrongSecret := c
	wrongSecret.Secret = "ccp_" + strings.Repeat("A", 43)
	unknown := c
	unknown.ParticipationID = "p_doesnotexist"
	noID := c
	noID.ParticipationID = ""
	// A web participant has no secret, so nothing authenticates as it.
	webAsCLI := cred{ChatID: chat, ParticipationID: webID, Secret: c.Secret}
	truncated := c
	truncated.Secret = c.Secret[:len(c.Secret)-1]
	for _, bad := range []cred{wrongSecret, unknown, noID, webAsCLI, truncated} {
		h.expect(h.send(bad, "x", ""), http.StatusUnauthorized, "invalid_credentials")
		h.expect(h.agent(bad, "GET", "/state", ""), http.StatusUnauthorized, "invalid_credentials")
	}
	h.expect(h.do("GET", "/api/v1/agent/chats/"+chat+"/state", ""), http.StatusUnauthorized, "invalid_credentials")

	h.expect(h.agent(c, "POST", "/leave", ""), http.StatusOK, "")
	// After leaving, the right secret is told so; a wrong one learns nothing.
	h.expect(h.send(c, "x", ""), http.StatusGone, "participation_left")
	h.expect(h.agent(c, "GET", "/messages", ""), http.StatusGone, "participation_left")
	h.expect(h.agent(c, "POST", "/leave", ""), http.StatusGone, "participation_left")
	h.expect(h.send(wrongSecret, "x", ""), http.StatusUnauthorized, "invalid_credentials")
	if got := h.textMessages(chat); len(got) != 0 {
		t.Fatalf("messages were created: %v", got)
	}

	// User tokens: unknown and revoked are told apart.
	h.expect(h.do("GET", "/api/v1/me", "", withBearer("cct_nope")), http.StatusUnauthorized, "unauthenticated")
	tokens := h.do("GET", "/api/v1/tokens", "", withSession(alice)).JSON()["tokens"].([]any)
	tokenID := tokens[0].(map[string]any)["token_id"].(string)
	h.expect(h.do("DELETE", "/api/v1/tokens/"+tokenID, "", withSession(alice), withOrigin(origin)), http.StatusNoContent, "")
	h.expect(h.do("POST", "/api/v1/chats/"+chat+"/participations", `{"display_name":"x"}`, withToken(alice)),
		http.StatusUnauthorized, "token_revoked")
	h.expect(h.do("GET", "/api/v1/me", "", withToken(alice)), http.StatusUnauthorized, "token_revoked")
}

// 5. A chat's credentials do not work in another chat.
func TestCredentialIsBoundToItsChat(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chatA := h.createChat(alice, "a")
	chatB := h.createChat(alice, "b")
	c := h.join(alice, chatA, "Claude")
	h.join(alice, chatB, "Claude")

	inB := c
	inB.ChatID = chatB
	h.expect(h.send(inB, "x", ""), http.StatusUnauthorized, "chat_mismatch")
	h.expect(h.agent(inB, "GET", "/messages", ""), http.StatusUnauthorized, "chat_mismatch")
	h.expect(h.agent(inB, "POST", "/leave", ""), http.StatusUnauthorized, "chat_mismatch")
	// Without the secret, even the mismatch stays hidden.
	inB.Secret = "ccp_" + strings.Repeat("B", 43)
	h.expect(h.send(inB, "x", ""), http.StatusUnauthorized, "invalid_credentials")

	if got := h.textMessages(chatB); len(got) != 0 {
		t.Fatalf("messages in b: %v", got)
	}
	// The credential still works where it belongs.
	h.expect(h.send(c, "x", ""), http.StatusCreated, "")
}

// 6. Retrying a send does not duplicate the message.
func TestSendIsIdempotent(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	a := h.join(alice, chat, "A")
	b := h.join(alice, chat, "B")

	first := h.send(a, "hello", "req-1")
	h.expect(first, http.StatusCreated, "")
	retry := h.send(a, "hello", "req-1")
	h.expect(retry, http.StatusOK, "")
	if first.JSON()["message_id"] != retry.JSON()["message_id"] || first.JSON()["sequence"] != retry.JSON()["sequence"] {
		t.Fatalf("retry = %s, first = %s", retry.Body, first.Body)
	}
	h.expect(h.send(a, "changed", "req-1"), http.StatusConflict, "request_id_conflict")
	// Request IDs are per participant.
	h.expect(h.send(b, "hello", "req-1"), http.StatusCreated, "")

	// Concurrent retries of one request still make one message.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.send(a, "burst", "req-2")
		}()
	}
	wg.Wait()
	if got := h.textMessages(chat); strings.Join(got, ",") != "hello,hello,burst" {
		t.Fatalf("messages = %v", got)
	}
	// Web sends are idempotent the same way.
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	h.expect(h.web(alice, chat, "POST", "/messages", `{"text":"web","client_request_id":"w1"}`), http.StatusCreated, "")
	h.expect(h.web(alice, chat, "POST", "/messages", `{"text":"web","client_request_id":"w1"}`), http.StatusOK, "")
	if got := h.textMessages(chat); len(got) != 4 {
		t.Fatalf("messages = %v", got)
	}
}

// 9. Destroying a chat ends every credential at once and its content can
// no longer be read.
func TestDestroyEndsEverything(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.account("alice"), h.account("bob")
	chat := h.createChat(alice, "t")
	c1 := h.join(alice, chat, "Claude")
	c2 := h.join(bob, chat, "Codex")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	h.expect(h.web(bob, chat, "POST", "/enter", ""), http.StatusOK, "")
	h.expect(h.send(c1, "secret plans", ""), http.StatusCreated, "")

	// A pending wait is cut short by the destruction.
	waited := make(chan response, 1)
	go func() { waited <- h.agent(c2, "GET", "/messages/wait?after_sequence=100&timeout_seconds=30", "") }()
	time.Sleep(50 * time.Millisecond)

	// Only the creator may destroy.
	h.expect(h.web(bob, chat, "DELETE", "", ""), http.StatusForbidden, "forbidden")
	h.expect(h.web(alice, chat, "DELETE", "", ""), http.StatusNoContent, "")

	select {
	case r := <-waited:
		h.expect(r, http.StatusGone, "chat_destroyed")
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after destruction")
	}
	for _, c := range []cred{c1, c2} {
		h.expect(h.agent(c, "GET", "/state", ""), http.StatusGone, "chat_destroyed")
		h.expect(h.agent(c, "GET", "/messages", ""), http.StatusGone, "chat_destroyed")
		h.expect(h.send(c, "x", ""), http.StatusGone, "chat_destroyed")
		h.expect(h.agent(c, "POST", "/leave", ""), http.StatusGone, "chat_destroyed")
		wrong := c
		wrong.Secret = "ccp_" + strings.Repeat("C", 43)
		h.expect(h.agent(wrong, "GET", "/state", ""), http.StatusUnauthorized, "invalid_credentials")
	}
	for _, a := range []account{alice, bob} {
		h.expect(h.web(a, chat, "GET", "/messages", ""), http.StatusGone, "chat_destroyed")
		h.expect(h.web(a, chat, "POST", "/enter", ""), http.StatusGone, "chat_destroyed")
	}
	h.expect(h.do("POST", "/api/v1/chats/"+chat+"/participations", `{"display_name":"x"}`, withToken(bob)),
		http.StatusGone, "chat_destroyed")
	h.expect(h.web(alice, chat, "DELETE", "", ""), http.StatusGone, "chat_destroyed")
	if ms := h.messages(chat); len(ms) != 0 {
		t.Fatalf("messages survived: %+v", ms)
	}

	// Account history keeps the metadata only.
	info := h.do("GET", "/api/v1/chats/"+chat, "", withSession(bob)).JSON()
	if info["destroy_reason"] != "creator" || info["destroyed_at"] == nil || info["online_count"] != nil {
		t.Fatalf("chat = %v", info)
	}
	ps := h.do("GET", "/api/v1/participations?chat_id="+chat, "", withToken(bob)).JSON()["participations"].([]any)
	if len(ps) != 2 {
		t.Fatalf("participations = %v", ps)
	}
	for _, p := range ps {
		if p.(map[string]any)["end_reason"] != "chat_destroyed" {
			t.Fatalf("participation = %v", p)
		}
	}
}

// 10. Credentials never appear in logs or error responses, and the
// database keeps only their hashes.
func TestCredentialsDoNotLeak(t *testing.T) {
	var logs bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lockedWriter{&mu, &logs}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	h := newHarnessIn(t, dir)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	created := h.do("POST", "/api/v1/tokens", `{"name":"cli"}`, withSession(alice), withOrigin(origin))
	h.expect(created, http.StatusCreated, "")
	newToken := created.JSON()["token"].(string)
	c := h.join(alice, chat, "Claude")
	secrets := []string{alice.session, alice.token, newToken, c.Secret}

	// Every kind of failure, carrying credentials that look plausible.
	bogus := c
	bogus.Secret = c.Secret[:len(c.Secret)-2] + "zz"
	other := h.createChat(alice, "other")
	elsewhere := c
	elsewhere.ChatID = other
	responses := []response{
		h.send(bogus, "x", ""),
		h.send(elsewhere, "x", ""),
		h.do("GET", "/api/v1/agent/chats/"+chat+"/state", "", withToken(alice),
			func(r *http.Request) { r.Header.Set(participationHeader, c.ParticipationID) }),
		h.do("GET", "/api/v1/me", "", withBearer(alice.token+"x")),
		h.do("GET", "/api/v1/me", "", withCred(c)),
		h.send(c, strings.Repeat("长", 16001), ""),
		h.do("GET", "/api/v1/participations", "", withToken(alice)),
		h.agent(c, "GET", "/state", ""),
	}
	h.expect(h.agent(c, "POST", "/leave", ""), http.StatusOK, "")
	responses = append(responses, h.send(c, "x", ""))
	h.expect(h.web(alice, chat, "DELETE", "", ""), http.StatusNoContent, "")
	responses = append(responses, h.send(c, "x", ""))

	for _, r := range responses {
		for _, s := range append(secrets, bogus.Secret, alice.token+"x") {
			if strings.Contains(r.Body.String(), s) || strings.Contains(fmt.Sprint(r.Header()), s) {
				t.Fatalf("response %d %s echoes a credential", r.ResponseRecorder.Code, r.Body)
			}
		}
	}
	if logs.Len() == 0 {
		t.Fatal("expected request logs")
	}
	h.store.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, s := range secrets {
		if strings.Contains(logs.String(), s) {
			t.Fatalf("log contains a credential:\n%s", logs.String())
		}
		for _, f := range files {
			raw, _ := os.ReadFile(f)
			if bytes.Contains(raw, []byte(s)) {
				t.Fatalf("%s stores a credential in plain text", filepath.Base(f))
			}
		}
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
