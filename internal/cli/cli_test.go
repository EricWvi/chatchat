package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatchat/internal/store"
)

func TestParticipantFlow(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("重构讨论")
	line, cred := h.join(chatID, "Claude")
	if cred.Server != h.srv.URL || cred.ChatID != chatID || !strings.HasPrefix(cred.Secret, "ccp_") {
		t.Fatalf("credential line: %+v", cred)
	}

	r := h.run(line+"\n第一行\n第二行\n", nil, "send")
	h.expect(r, 0)
	if !strings.Contains(r.stdout, "Sent #2") {
		t.Fatalf("send: %s", r.stdout)
	}
	h.expect(h.run(line, nil, "send", "--text", "第三行"), 0)

	r = h.run(line, nil, "read", "--after", "1")
	h.expect(r, 0)
	for _, want := range []string{"#2 ", "  第一行\n  第二行\n", "#3 ", "  第三行", "--after 3"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("read output lacks %q:\n%s", want, r.stdout)
		}
	}

	r = h.run(line, nil, "--json", "read", "--after", "2")
	h.expect(r, 0)
	var page messagePage
	if err := json.Unmarshal([]byte(r.stdout), &page); err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "第三行" {
		t.Fatalf("read --json: %s (%v)", r.stdout, err)
	}

	r = h.run(line, nil, "state")
	h.expect(r, 0)
	for _, want := range []string{"重构讨论", "Latest message: #3", "Summary: none yet", "Claude (", "(you) CLI online", "read --after 0"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("state output lacks %q:\n%s", want, r.stdout)
		}
	}

	r = h.run(line, nil, "propose-end", "--reason", "讨论完了")
	h.expect(r, 0)
	if !strings.Contains(r.stdout, "#4") {
		t.Fatalf("propose-end: %s", r.stdout)
	}

	r = h.run(line, nil, "leave")
	h.expect(r, 0)
	entries, err := readHistory(h.historyFile())
	if err != nil || len(entries) != 1 || entries[0].LeftAt == nil {
		t.Fatalf("history after leave: %+v (%v)", entries, err)
	}
	h.expect(h.run(line, nil, "state"), exitLeft)
}

func TestWait(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("t")
	line, _ := h.join(chatID, "A")
	other, _ := h.join(chatID, "B")

	r := h.run(line, nil, "wait", "--after", "2", "--timeout", "0")
	h.expect(r, 0)
	if !strings.Contains(r.stdout, "Wait timed out") {
		t.Fatalf("wait timeout: %s", r.stdout)
	}

	done := make(chan result)
	go func() { done <- h.run(line, nil, "--json", "wait", "--after", "2", "--timeout", "20") }()
	time.Sleep(200 * time.Millisecond)
	h.expect(h.run(other, nil, "send", "--text", "hi"), 0)
	select {
	case r := <-done:
		h.expect(r, 0)
		var page messagePage
		if err := json.Unmarshal([]byte(r.stdout), &page); err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "hi" {
			t.Fatalf("wait: %s", r.stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return after a new message")
	}

	h.expect(h.run(line, nil, "wait"), exitUsage)
}

// dropMessagePosts makes the server handle the next n message sends but
// cut the connection before answering, as a network failure would.
func (h *harness) dropMessagePosts(n int32) {
	api := h.handler
	var left atomic.Int32
	left.Store(n)
	h.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/messages") && left.Add(-1) >= 0 {
			api.ServeHTTP(httptest.NewRecorder(), r)
			panic(http.ErrAbortHandler)
		}
		api.ServeHTTP(w, r)
	})
}

// Requirement 6: retrying a send does not duplicate the message.
func TestSendRetryReusesRequestID(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("t")
	line, _ := h.join(chatID, "A")

	h.dropMessagePosts(2)
	r := h.run(line, nil, "send", "--text", "只发一次")
	h.expect(r, 0)
	if !strings.Contains(r.stdout, "duplicate request") {
		t.Fatalf("the third attempt should get the first message back: %s", r.stdout)
	}
	if n := countText(t, h, chatID); n != 1 {
		t.Fatalf("%d text messages, want 1", n)
	}
}

func TestSendFailurePrintsRequestID(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("t")
	line, _ := h.join(chatID, "A")
	api := h.handler

	h.dropMessagePosts(3)
	r := h.run(line, nil, "--json", "send", "--text", "网络不好")
	h.expect(r, exitError)
	var e struct {
		Code      string `json:"code"`
		RequestID string `json:"client_request_id"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &e); err != nil || e.Code != "network" || e.RequestID == "" {
		t.Fatalf("stderr: %s (%v)", r.stderr, err)
	}

	h.handler = api
	r = h.run(line, nil, "send", "--text", "网络不好", "--request-id", e.RequestID)
	h.expect(r, 0)
	if !strings.Contains(r.stdout, "duplicate request") {
		t.Fatalf("retry with the printed id: %s", r.stdout)
	}
	if n := countText(t, h, chatID); n != 1 {
		t.Fatalf("%d text messages, want 1", n)
	}

	r = h.run(line, nil, "send", "--text", "另一段", "--request-id", e.RequestID)
	h.expect(r, exitConflict)
	if !strings.Contains(r.stderr, e.RequestID) {
		t.Fatalf("a failed send prints its request id: %s", r.stderr)
	}
}

func countText(t *testing.T, h *harness, chatID string) int {
	t.Helper()
	ms, _, err := h.store.Messages(context.Background(), chatID, 0, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range ms {
		if m.Kind == store.KindText {
			n++
		}
	}
	return n
}

func TestExitCodes(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("t")
	otherChat := h.createChat("other")
	line, cred := h.join(chatID, "A")

	marshal := func(c credential) string {
		b, _ := json.Marshal(c)
		return string(b)
	}
	wrongSecret := cred
	wrongSecret.Secret = "ccp_wrong"
	otherChatCred := cred
	otherChatCred.ChatID = otherChat
	userToken := cred
	userToken.Secret = h.token

	cases := []struct {
		name  string
		stdin string
		env   []string
		args  []string
		want  int
	}{
		{"unknown command", "", nil, []string{"nope"}, exitUsage},
		{"join without name", "", nil, []string{"join", chatID}, exitUsage},
		{"join bad chat ref", "", nil, []string{"join", "x y", "--name", "A"}, exitUsage},
		{"send without text", line, nil, []string{"send"}, exitUsage},
		{"send with both bodies", line + "\nbody", nil, []string{"send", "--text", "x"}, exitUsage},
		{"no credential line", "", nil, []string{"state"}, exitCredentials},
		{"malformed credential line", "{not json", nil, []string{"state"}, exitCredentials},
		{"wrong secret", marshal(wrongSecret), nil, []string{"state"}, exitCredentials},
		{"credential of another chat", marshal(otherChatCred), nil, []string{"state"}, exitCredentials},
		{"user token as secret", marshal(userToken), nil, []string{"send", "--text", "x"}, exitCredentials},
		{"no user token", "", []string{tokenEnv + "="}, []string{"join", chatID, "--name", "A"}, exitCredentials},
		{"unknown user token", "", []string{tokenEnv + "=cct_nope"}, []string{"join", chatID, "--name", "A"}, exitCredentials},
		{"secret as user token", "", []string{tokenEnv + "=" + cred.Secret}, []string{"join", chatID, "--name", "A"}, exitCredentials},
		{"no such chat", "", nil, []string{"join", "c_missing", "--name", "A"}, exitError},
		{"no server", "", []string{serverEnv + "="}, []string{"join", chatID, "--name", "A"}, exitError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.run(tc.stdin, tc.env, tc.args...)
			h.expect(r, tc.want)
			for _, s := range []string{cred.Secret, h.token} {
				if strings.Contains(r.stdout+r.stderr, s) {
					t.Fatalf("output leaks a credential:\n%s%s", r.stdout, r.stderr)
				}
			}
		})
	}

	// Credentials that were valid stop working with distinct codes.
	if err := h.store.RevokeUserToken(context.Background(), h.user.ID, tokenID(t, h)); err != nil {
		t.Fatal(err)
	}
	h.expect(h.run("", nil, "join", chatID, "--name", "A"), exitCredentials)
	if _, err := h.store.DestroyChat(context.Background(), chatID, "creator"); err != nil {
		t.Fatal(err)
	}
	r := h.run(line, nil, "read")
	h.expect(r, exitDestroyed)
	if !strings.Contains(r.stderr, "the chat has been destroyed") {
		t.Fatalf("stderr: %s", r.stderr)
	}
}

func tokenID(t *testing.T, h *harness) string {
	t.Helper()
	ts, err := h.store.ListUserTokens(context.Background(), h.user.ID)
	if err != nil || len(ts) != 1 {
		t.Fatalf("tokens: %v %v", ts, err)
	}
	return ts[0].ID
}

func TestProtocolMismatch(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("t")
	line, _ := h.join(chatID, "A")
	api := h.handler
	var sent atomic.Bool
	h.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/meta" {
			w.Header().Set("Chatchat-Protocol", "2")
			w.Write([]byte(`{"protocol_version":2,"limits":{}}`))
			return
		}
		sent.Store(true)
		api.ServeHTTP(w, r)
	})
	r := h.run(line, nil, "send", "--text", "x")
	h.expect(r, exitProtocol)
	if sent.Load() {
		t.Fatal("the CLI went on after an incompatible protocol version")
	}
	h.expect(h.run("", nil, "config", "check"), exitProtocol)
}

func TestNotChatchatServer(t *testing.T) {
	h := newHarness(t)
	other := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(other.Close)
	r := h.run("", []string{serverEnv + "=" + other.URL}, "config", "check")
	h.expect(r, exitError)
	if !strings.Contains(r.stdout, "does not look like a chatchat server") {
		t.Fatalf("stdout: %s", r.stdout)
	}
}

func TestJoinAcceptsLink(t *testing.T) {
	h := newHarness(t)
	chatID := h.createChat("t")
	_, cred := h.join("https://chatchat.test/c/"+chatID, "A")
	if cred.ChatID != chatID {
		t.Fatalf("joined %s, want %s", cred.ChatID, chatID)
	}
}

func TestConfig(t *testing.T) {
	h := newHarness(t)
	noEnv := []string{serverEnv + "=", tokenEnv + "="}

	r := h.run("", noEnv, "config", "check")
	h.expect(r, exitError)

	h.expect(h.run("ccp_x\n", noEnv, "config", "set-token"), exitUsage)
	h.expect(h.run("", noEnv, "config", "set-token", h.token), exitUsage)
	r = h.run(h.token+"\n", noEnv, "--server", h.srv.URL+"/", "config", "set-token")
	h.expect(r, 0)
	if strings.Contains(r.stdout+r.stderr, h.token) {
		t.Fatal("set-token echoed the token")
	}
	st, err := os.Stat(h.configFile())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", st, err)
	}
	c, err := loadConfig(h.configFile())
	if err != nil || c.Token != h.token || c.Server != h.srv.URL {
		t.Fatalf("config: %+v %v", c, err)
	}

	r = h.run("", noEnv, "config", "check")
	h.expect(r, 0)
	if !strings.Contains(r.stdout, "Alice <alice@example.com>") || strings.Contains(r.stdout, h.token) {
		t.Fatalf("check: %s", r.stdout)
	}
	r = h.run("", noEnv, "--json", "config", "check")
	h.expect(r, 0)
	var report map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &report); err != nil || report["ok"] != true {
		t.Fatalf("check --json: %s", r.stdout)
	}

	// The environment wins over the file.
	h.expect(h.run("", []string{tokenEnv + "=cct_nope"}, "config", "check"), exitCredentials)
}

// Requirements 2 and 10: neither the local history nor the server's
// participation records give out a secret, and nothing the CLI writes or
// prints, apart from join's credential line, holds one.
func TestNoCredentialLeaks(t *testing.T) {
	h := newHarness(t)
	noEnv := []string{serverEnv + "=", tokenEnv + "="}
	h.expect(h.run(h.token+"\n", noEnv, "--server", h.srv.URL, "config", "set-token"), 0)
	chatID := h.createChat("t")

	var secrets []string
	var lines []string
	for _, name := range []string{"A", "B"} {
		r := h.run("", noEnv, "join", chatID, "--name", name)
		h.expect(r, 0)
		var c credential
		json.Unmarshal([]byte(r.stdout), &c)
		secrets = append(secrets, c.Secret)
		lines = append(lines, strings.TrimSpace(r.stdout))
		if strings.Contains(r.stderr, c.Secret) {
			t.Fatal("join printed the secret on stderr")
		}
	}
	if secrets[0] == secrets[1] {
		t.Fatal("two joins share a secret")
	}

	var outputs []string
	for _, run := range []struct {
		stdin string
		args  []string
	}{
		{lines[0], []string{"send", "--text", "hello"}},
		{lines[0], []string{"state"}},
		{lines[0], []string{"--json", "state"}},
		{lines[0], []string{"read"}},
		{lines[1], []string{"leave"}},
		{lines[1], []string{"send", "--text", "after leave"}},
		{"", []string{"history"}},
		{"", []string{"--json", "history"}},
		{"", []string{"history", "--remote"}},
		{"", []string{"--json", "history", "--remote"}},
		{"", []string{"config", "check"}},
	} {
		r := h.run(run.stdin, noEnv, run.args...)
		outputs = append(outputs, r.stdout, r.stderr)
	}

	files := h.localFiles()
	if len(files) == 0 {
		t.Fatal("the CLI wrote no local files")
	}
	for _, s := range secrets {
		for _, out := range outputs {
			if strings.Contains(out, s) {
				t.Fatalf("output leaks a secret:\n%s", out)
			}
		}
		for path, content := range files {
			if strings.Contains(content, s) {
				t.Fatalf("%s holds a secret", path)
			}
		}
	}
	for _, out := range outputs {
		if strings.Contains(out, h.token) {
			t.Fatalf("output leaks the user token:\n%s", out)
		}
	}
	for path, content := range files {
		if path != h.configFile() && strings.Contains(content, h.token) {
			t.Fatalf("%s holds the user token", path)
		}
	}

	hist := files[h.historyFile()]
	for _, want := range []string{`"chat_id":"` + chatID, `"display_name":"A"`, `"display_name":"B"`, `"left_at":"`, `"left_at":null`} {
		if !strings.Contains(hist, want) {
			t.Fatalf("history lacks %s:\n%s", want, hist)
		}
	}
	st, err := os.Stat(h.historyFile())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode: %v %v", st, err)
	}
}
