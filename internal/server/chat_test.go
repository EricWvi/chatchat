package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMetaAndProtocolHeader(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/api/v1/meta", "")
	h.expect(r, http.StatusOK, "")
	if r.Header().Get("Chatchat-Protocol") != "1" {
		t.Fatalf("header = %v", r.Header())
	}
	var meta struct {
		ProtocolVersion int            `json:"protocol_version"`
		Limits          map[string]int `json:"limits"`
	}
	json.Unmarshal(r.Body.Bytes(), &meta)
	want := map[string]int{
		"presence_lease_seconds": 90, "idle_destroy_after_seconds": 1800, "max_chat_lifetime_seconds": 86400,
		"wait_max_seconds": 60, "page_size_default": 50, "page_size_max": 200,
		"message_max_chars": 16000, "display_name_max_chars": 48, "summary_threshold": 20,
	}
	if meta.ProtocolVersion != 1 || fmt.Sprint(meta.Limits) != fmt.Sprint(want) {
		t.Fatalf("meta = %s", r.Body)
	}
	// Errors carry the header too.
	if r := h.do("GET", "/api/v1/nope", ""); r.Header().Get("Chatchat-Protocol") != "1" || r.Code() != "not_found" {
		t.Fatalf("unknown endpoint: %d %s", r.ResponseRecorder.Code, r.Body)
	}
}

func TestUserTokens(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	h.clock.Advance(time.Minute)
	created := h.do("POST", "/api/v1/tokens", `{"name":"  MacBook  "}`, withSession(alice), withOrigin(origin))
	h.expect(created, http.StatusCreated, "")
	token := created.JSON()["token"].(string)
	if !strings.HasPrefix(token, "cct_") || created.JSON()["name"] != "MacBook" {
		t.Fatalf("created = %s", created.Body)
	}
	h.expect(h.do("POST", "/api/v1/tokens", `{"name":""}`, withSession(alice), withOrigin(origin)),
		http.StatusBadRequest, "invalid_request")

	me := h.do("GET", "/api/v1/me", "", withBearer(token))
	h.expect(me, http.StatusOK, "")
	if me.JSON()["name"] != "alice" {
		t.Fatalf("me = %s", me.Body)
	}

	list := h.do("GET", "/api/v1/tokens", "", withSession(alice))
	if strings.Contains(list.Body.String(), "cct_") {
		t.Fatalf("list shows a token: %s", list.Body)
	}
	tokens := list.JSON()["tokens"].([]any)
	if len(tokens) != 2 {
		t.Fatalf("tokens = %s", list.Body)
	}
	// The newest token is first and records its use.
	first := tokens[0].(map[string]any)
	if first["name"] != "MacBook" || first["last_used_at"] == nil {
		t.Fatalf("tokens = %s", list.Body)
	}

	// Someone else cannot revoke it.
	bob := h.account("bob")
	id := first["token_id"].(string)
	h.expect(h.do("DELETE", "/api/v1/tokens/"+id, "", withSession(bob), withOrigin(origin)), http.StatusNotFound, "not_found")
	// A cross-site request cannot either.
	h.expect(h.do("DELETE", "/api/v1/tokens/"+id, "", withSession(alice), withOrigin("https://evil.test")),
		http.StatusForbidden, "untrusted_origin")
	h.expect(h.do("DELETE", "/api/v1/tokens/"+id, "", withSession(alice), withOrigin(origin)), http.StatusNoContent, "")
	if n := len(h.do("GET", "/api/v1/tokens", "", withSession(alice)).JSON()["tokens"].([]any)); n != 1 {
		t.Fatalf("tokens after revoke = %d", n)
	}
}

func TestCreateAndListChats(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.account("alice"), h.account("bob")

	r := h.do("POST", "/api/v1/chats", `{"title":"  重构讨论 ","goal":"定下接口"}`, withToken(alice))
	h.expect(r, http.StatusCreated, "")
	c := r.JSON()
	chat := c["chat_id"].(string)
	if c["title"] != "重构讨论" || c["url"] != origin+"/c/"+chat || c["expires_at"] != "2026-10-27T08:00:00Z" {
		t.Fatalf("created = %s", r.Body)
	}
	for _, body := range []string{`{"title":""}`, `{"title":"` + strings.Repeat("长", 81) + `"}`,
		`{"title":"x","goal":"` + strings.Repeat("长", 2001) + `"}`, `{"title":"a\nb"}`, `not json`} {
		h.expect(h.do("POST", "/api/v1/chats", body, withToken(alice)), http.StatusBadRequest, "invalid_request")
	}
	// Cookie writes need a same-origin request.
	h.expect(h.do("POST", "/api/v1/chats", `{"title":"x"}`, withSession(alice), withOrigin("https://evil.test")),
		http.StatusForbidden, "untrusted_origin")

	older := h.createChat(alice, "older")
	h.clock.Advance(time.Minute)
	gone := h.createChat(alice, "gone")
	h.clock.Advance(time.Minute)
	// Bob only sees the chat he joined.
	h.join(bob, chat, "Codex")
	h.expect(h.web(alice, gone, "DELETE", "", ""), http.StatusNoContent, "")

	list := h.do("GET", "/api/v1/chats", "", withSession(alice)).JSON()["chats"].([]any)
	var ids []string
	for _, item := range list {
		ids = append(ids, item.(map[string]any)["chat_id"].(string))
	}
	// Live chats by latest activity (the join just now), destroyed last.
	if !slices.Equal(ids, []string{chat, older, gone}) {
		t.Fatalf("order = %v", ids)
	}
	first := list[0].(map[string]any)
	if first["online_count"] != 1.0 || first["active_count"] != 1.0 || first["idle_destroy_at"] != nil ||
		first["created_by_me"] != true {
		t.Fatalf("first = %v", first)
	}
	second := list[1].(map[string]any)
	if second["online_count"] != 0.0 || second["idle_destroy_at"] != "2026-10-26T08:30:00Z" {
		t.Fatalf("second = %v", second)
	}
	bobs := h.do("GET", "/api/v1/chats", "", withToken(bob)).JSON()["chats"].([]any)
	if len(bobs) != 1 || bobs[0].(map[string]any)["created_by_me"] != false {
		t.Fatalf("bob's chats = %v", bobs)
	}

	// Paging walks the same order.
	page := h.do("GET", "/api/v1/chats?limit=2", "", withSession(alice)).JSON()
	if len(page["chats"].([]any)) != 2 || page["next_cursor"] != "2" {
		t.Fatalf("page = %v", page)
	}
	page = h.do("GET", "/api/v1/chats?limit=2&cursor=2", "", withSession(alice)).JSON()
	if len(page["chats"].([]any)) != 1 || page["next_cursor"] != nil {
		t.Fatalf("page 2 = %v", page)
	}

	h.expect(h.do("GET", "/api/v1/chats/c_nope", "", withSession(alice)), http.StatusNotFound, "chat_not_found")
	h.expect(h.do("POST", "/api/v1/chats/c_nope/participations", `{"display_name":"x"}`, withToken(alice)),
		http.StatusNotFound, "chat_not_found")
	h.expect(h.web(alice, "c_nope", "POST", "/enter", ""), http.StatusNotFound, "chat_not_found")
}

func TestJoinValidatesDisplayName(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	for _, name := range []string{"", "   ", strings.Repeat("名", 49), "a\tb"} {
		body, _ := json.Marshal(map[string]string{"display_name": name})
		h.expect(h.do("POST", "/api/v1/chats/"+chat+"/participations", string(body), withToken(alice)),
			http.StatusBadRequest, "invalid_request")
	}
	if c := h.join(alice, chat, " Claude "); c.DisplayName != "Claude" {
		t.Fatalf("display name = %q", c.DisplayName)
	}
}

func TestMessagesAreNumberedAndPaged(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude") // sequence 1 announces the join
	for i := range 9 {
		h.expect(h.send(c, fmt.Sprintf("m%d", i+2), ""), http.StatusCreated, "")
	}

	page := func(query string) ([]int, bool, int) {
		t.Helper()
		r := h.agent(c, "GET", "/messages"+query, "")
		h.expect(r, http.StatusOK, "")
		var out struct {
			Messages []struct {
				Sequence int    `json:"sequence"`
				Kind     string `json:"kind"`
				Text     string `json:"text"`
			} `json:"messages"`
			HasMore bool `json:"has_more"`
			Latest  int  `json:"latest_sequence"`
		}
		json.Unmarshal(r.Body.Bytes(), &out)
		var seqs []int
		for _, m := range out.Messages {
			seqs = append(seqs, m.Sequence)
		}
		return seqs, out.HasMore, out.Latest
	}

	seqs, more, latest := page("")
	if !slices.Equal(seqs, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) || more || latest != 10 {
		t.Fatalf("all = %v %v %d", seqs, more, latest)
	}
	if seqs, more, _ = page("?after_sequence=3&limit=4"); !slices.Equal(seqs, []int{4, 5, 6, 7}) || !more {
		t.Fatalf("after = %v %v", seqs, more)
	}
	if seqs, more, _ = page("?after_sequence=7&limit=4"); !slices.Equal(seqs, []int{8, 9, 10}) || more {
		t.Fatalf("tail = %v %v", seqs, more)
	}
	if seqs, more, _ = page("?before_sequence=11&limit=3"); !slices.Equal(seqs, []int{8, 9, 10}) || !more {
		t.Fatalf("before = %v %v", seqs, more)
	}
	if seqs, more, _ = page("?before_sequence=3&limit=3"); !slices.Equal(seqs, []int{1, 2}) || more {
		t.Fatalf("head = %v %v", seqs, more)
	}
	// Limits above the maximum are capped rather than refused.
	if seqs, _, _ = page("?limit=1000"); len(seqs) != 10 {
		t.Fatalf("capped = %v", seqs)
	}
	for _, q := range []string{"?after_sequence=1&before_sequence=5", "?after_sequence=-1", "?limit=0", "?before_sequence=0", "?limit=x"} {
		h.expect(h.agent(c, "GET", "/messages"+q, ""), http.StatusBadRequest, "invalid_request")
	}

	// Blank and oversized messages are refused without using a sequence.
	h.expect(h.send(c, "  \n ", ""), http.StatusBadRequest, "invalid_request")
	h.expect(h.send(c, strings.Repeat("长", 16001), ""), http.StatusRequestEntityTooLarge, "too_large")
	h.expect(h.send(c, strings.Repeat("长", 16000), ""), http.StatusCreated, "")
	if _, _, latest = page("?after_sequence=10"); latest != 11 {
		t.Fatalf("latest = %d", latest)
	}
	// Text is stored exactly as sent.
	h.expect(h.send(c, "  **Markdown**\n- item\n", ""), http.StatusCreated, "")
	if got := h.textMessages(chat); got[len(got)-1] != "  **Markdown**\n- item\n" {
		t.Fatalf("text = %q", got[len(got)-1])
	}
}

func TestSystemMessages(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	c := h.join(alice, chat, "Claude")
	r := h.agent(c, "POST", "/propose-end", `{"reason":"结论已定"}`)
	h.expect(r, http.StatusCreated, "")
	if r.JSON()["kind"] != "system" || r.JSON()["text"] != "建议结束：结论已定" {
		t.Fatalf("propose-end = %s", r.Body)
	}
	h.expect(h.agent(c, "POST", "/propose-end", ""), http.StatusCreated, "")
	left := h.agent(c, "POST", "/leave", "")
	h.expect(left, http.StatusOK, "")
	if left.JSON()["participation_id"] != c.ParticipationID || left.JSON()["left_at"] == nil {
		t.Fatalf("leave = %s", left.Body)
	}
	var got []string
	for _, m := range h.messages(chat) {
		if m.Kind != "system" || m.ParticipationID != c.ParticipationID || m.DisplayName != "Claude" {
			t.Fatalf("message = %+v", m)
		}
		got = append(got, m.Text)
	}
	if strings.Join(got, "|") != "加入了 chat|建议结束：结论已定|建议结束|退出了 chat" {
		t.Fatalf("system messages = %v", got)
	}
}

func TestWebParticipant(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.account("alice"), h.account("bob")
	chat := h.createChat(alice, "t")

	// Not entered yet.
	h.expect(h.web(bob, chat, "GET", "/state", ""), http.StatusForbidden, "forbidden")

	first := h.web(bob, chat, "POST", "/enter", "")
	h.expect(first, http.StatusOK, "")
	id := first.JSON()["participation_id"].(string)
	if first.JSON()["kind"] != "web" || first.JSON()["status"] != "online" || first.JSON()["display_name"] != "bob" {
		t.Fatalf("enter = %s", first.Body)
	}
	// Entering again (a reload, another tab) is the same participant and
	// is not announced again.
	again := h.web(bob, chat, "POST", "/enter", "")
	if again.JSON()["participation_id"] != id || len(h.messages(chat)) != 1 {
		t.Fatalf("re-enter = %s, messages = %v", again.Body, h.messages(chat))
	}

	h.expect(h.web(bob, chat, "POST", "/messages", `{"text":"hi"}`), http.StatusCreated, "")
	state := h.web(bob, chat, "GET", "/state", "").JSON()
	if state["you"].(map[string]any)["participation_id"] != id || state["chat"].(map[string]any)["created_by_me"] != false {
		t.Fatalf("state = %v", state)
	}
	if h.web(alice, chat, "POST", "/enter", "").JSON()["participation_id"] == id {
		t.Fatal("alice got bob's participant")
	}

	h.expect(h.web(bob, chat, "POST", "/leave", ""), http.StatusOK, "")
	h.expect(h.web(bob, chat, "GET", "/state", ""), http.StatusGone, "participation_left")
	// Coming back revives the same participant and says so.
	back := h.web(bob, chat, "POST", "/enter", "")
	if back.JSON()["participation_id"] != id || back.JSON()["status"] != "online" || back.JSON()["left_at"] != nil {
		t.Fatalf("back = %s", back.Body)
	}
	var texts []string
	for _, m := range h.messages(chat) {
		texts = append(texts, m.Text)
	}
	if strings.Join(texts, "|") != "加入了 chat|hi|加入了 chat|退出了 chat|加入了 chat" {
		t.Fatalf("messages = %v", texts)
	}
	// Web writes need a same-origin request.
	r := h.do("POST", "/api/v1/web/chats/"+chat+"/messages", `{"text":"x"}`, withSession(bob), withOrigin("https://evil.test"))
	h.expect(r, http.StatusForbidden, "untrusted_origin")
	h.expect(h.do("GET", "/api/v1/web/chats/"+chat+"/state", ""), http.StatusUnauthorized, "unauthenticated")
}

func TestWaitReturnsOnNewMessages(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	a := h.join(alice, chat, "A")
	b := h.join(alice, chat, "B")

	// Messages already there come back at once.
	r := h.agent(a, "GET", "/messages/wait?after_sequence=1", "")
	h.expect(r, http.StatusOK, "")
	if ms := r.JSON()["messages"].([]any); len(ms) != 1 || ms[0].(map[string]any)["sequence"] != 2.0 {
		t.Fatalf("wait = %s", r.Body)
	}

	done := make(chan response, 1)
	go func() { done <- h.agent(a, "GET", "/messages/wait?after_sequence=2&timeout_seconds=10", "") }()
	time.Sleep(50 * time.Millisecond)
	h.expect(h.send(b, "ping", ""), http.StatusCreated, "")
	select {
	case r := <-done:
		h.expect(r, http.StatusOK, "")
		ms := r.JSON()["messages"].([]any)
		if len(ms) != 1 || ms[0].(map[string]any)["text"] != "ping" || r.JSON()["latest_sequence"] != 3.0 {
			t.Fatalf("wait = %s", r.Body)
		}
		if _, ok := r.JSON()["changed"]; ok {
			t.Fatalf("CLI wait reports changed: %s", r.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}

	h.expect(h.agent(a, "GET", "/messages/wait", ""), http.StatusBadRequest, "invalid_request")
}

func TestWaitTimesOutAndIgnoresOtherChanges(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	a := h.join(alice, chat, "A")

	start := time.Now()
	r := h.agent(a, "GET", "/messages/wait?after_sequence=2&timeout_seconds=1", "")
	h.expect(r, http.StatusOK, "")
	if ms := r.JSON()["messages"].([]any); len(ms) != 0 || r.JSON()["latest_sequence"] != 2.0 ||
		time.Since(start) < 900*time.Millisecond {
		t.Fatalf("wait = %s after %v", r.Body, time.Since(start))
	}

	// Alice's web participant coming back online is not a message, so it
	// does not end the CLI wait.
	h.clock.Advance(2 * time.Minute)
	done := make(chan response, 1)
	go func() { done <- h.agent(a, "GET", "/messages/wait?after_sequence=2&timeout_seconds=1", "") }()
	time.Sleep(50 * time.Millisecond)
	h.expect(h.web(alice, chat, "GET", "/state", ""), http.StatusOK, "")
	start = time.Now()
	if r := <-done; len(r.JSON()["messages"].([]any)) != 0 || time.Since(start) < 500*time.Millisecond {
		t.Fatalf("woken by a participant change: %s", r.Body)
	}
}

func TestWebWaitReportsChanges(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "") // sequence 1
	c := h.join(alice, chat, "Claude")                                    // sequence 2
	h.srv.Sweep(t.Context())                                              // baseline for lapsed leases

	// wait starts a web wait after sequence 2, runs trigger and returns
	// what the wait answered.
	wait := func(trigger func()) map[string]any {
		t.Helper()
		done := make(chan response, 1)
		go func() { done <- h.web(alice, chat, "GET", "/messages/wait?after_sequence=2&timeout_seconds=5", "") }()
		time.Sleep(50 * time.Millisecond)
		trigger()
		select {
		case r := <-done:
			h.expect(r, http.StatusOK, "")
			return r.JSON()
		case <-time.After(5 * time.Second):
			t.Fatal("wait did not return")
			return nil
		}
	}

	// Leases lapsing change the participants without adding a message.
	got := wait(func() {
		h.clock.Advance(91 * time.Second)
		h.srv.Sweep(t.Context())
	})
	if fmt.Sprint(got["changed"]) != "[participants]" || len(got["messages"].([]any)) != 0 {
		t.Fatalf("lapse = %v", got)
	}

	// A message wakes it with the message.
	h.expect(h.agent(c, "GET", "/state", ""), http.StatusOK, "") // Claude back online first
	got = wait(func() { h.send(c, "hi", "") })
	if fmt.Sprint(got["changed"]) != "[messages]" || len(got["messages"].([]any)) != 1 {
		t.Fatalf("message = %v", got)
	}

	// A timeout reports no change.
	r := h.web(alice, chat, "GET", "/messages/wait?after_sequence=3&timeout_seconds=0", "")
	if fmt.Sprint(r.JSON()["changed"]) != "[]" {
		t.Fatalf("timeout = %s", r.Body)
	}
}

// A change made while the page was not waiting is reported by its next
// wait, which passes the state_version it has seen.
func TestWebWaitReportsChangesSinceVersion(t *testing.T) {
	h := newHarness(t)
	alice := h.account("alice")
	chat := h.createChat(alice, "t")
	h.expect(h.web(alice, chat, "POST", "/enter", ""), http.StatusOK, "")
	st := h.web(alice, chat, "GET", "/state", "")
	h.expect(st, http.StatusOK, "")
	seen := st.JSON()["state_version"].(string)
	waitSince := func(version string) map[string]any {
		t.Helper()
		r := h.web(alice, chat, "GET", "/messages/wait?after_sequence=1&timeout_seconds=0&state_version="+url.QueryEscape(version), "")
		h.expect(r, http.StatusOK, "")
		return r.JSON()
	}

	got := waitSince(seen)
	if fmt.Sprint(got["changed"]) != "[]" || got["state_version"] != seen {
		t.Fatalf("nothing changed = %v", got)
	}

	// Claude joins between two waits: a message and a participant change.
	h.join(alice, chat, "Claude")
	got = waitSince(seen)
	if fmt.Sprint(got["changed"]) != "[messages participants]" || got["state_version"] == seen {
		t.Fatalf("join = %v", got)
	}
	seen = got["state_version"].(string)
	if got := waitSince(seen); fmt.Sprint(got["changed"]) != "[messages]" {
		t.Fatalf("after catching up on participants = %v", got)
	}

	// A version from another process means everything may have changed.
	if got := waitSince("old.0.0"); fmt.Sprint(got["changed"]) != "[messages summary participants]" {
		t.Fatalf("foreign version = %v", got)
	}
}
