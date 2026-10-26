package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"chatchat/internal/auth"
	"chatchat/internal/config"
	"chatchat/internal/store"
)

const origin = "https://chatchat.test"

// clock is a settable time source shared by the store and the tests.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type harness struct {
	t     *testing.T
	srv   *Server
	store *store.Store
	clock *clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessIn(t, t.TempDir())
}

// newHarnessIn keeps the database in dir.
func newHarnessIn(t *testing.T, dir string) *harness {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := &clock{now: time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)}
	st.SetClock(clk.Now)
	// The provider is discovered lazily, so these tests never reach it.
	authn := auth.New(st, auth.Config{PublicURL: origin, Issuer: "https://idp.invalid", ClientID: "x", ClientSecret: "y"})
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>chatchat</title>")}}
	srv, err := New(st, authn, web, Options{Version: "test", PublicURL: origin, Limits: config.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, store: st, clock: clk}
}

// account is a signed-in user with a browser session and a user token.
type account struct {
	store.User
	session string
	token   string
}

func (h *harness) account(subject string) account {
	h.t.Helper()
	ctx := context.Background()
	u, err := h.store.UpsertUser(ctx, store.Identity{Issuer: "https://idp.test", Subject: subject, Name: subject})
	if err != nil {
		h.t.Fatal(err)
	}
	session, err := h.store.CreateSession(ctx, u.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	_, token, err := h.store.CreateUserToken(ctx, u.ID, "laptop")
	if err != nil {
		h.t.Fatal(err)
	}
	return account{User: u, session: session, token: token}
}

// cred is the credential line `chatchat join` prints.
type cred struct {
	ChatID          string `json:"chat_id"`
	ParticipationID string `json:"participation_id"`
	Secret          string `json:"participation_secret"`
	DisplayName     string `json:"display_name"`
}

// reqOpt decorates a request.
type reqOpt func(*http.Request)

func withSession(a account) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: a.session}) }
}

func withBearer(token string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withToken(a account) reqOpt { return withBearer(a.token) }

func withCred(c cred) reqOpt {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+c.Secret)
		r.Header.Set(participationHeader, c.ParticipationID)
	}
}

func withOrigin(o string) reqOpt { return func(r *http.Request) { r.Header.Set("Origin", o) } }

type response struct {
	*httptest.ResponseRecorder
}

// JSON decodes the body into a generic map.
func (r response) JSON() map[string]any {
	var v map[string]any
	json.Unmarshal(r.Body.Bytes(), &v)
	return v
}

func (r response) Code() string {
	s, _ := r.JSON()["code"].(string)
	return s
}

func (h *harness) do(method, path, body string, opts ...reqOpt) response {
	h.t.Helper()
	req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return response{rec}
}

// expect fails unless the response has the status and, for errors, the
// code.
func (h *harness) expect(r response, status int, code string) {
	h.t.Helper()
	if r.ResponseRecorder.Code != status || (code != "" && r.Code() != code) {
		h.t.Fatalf("got %d %s, want %d %s", r.ResponseRecorder.Code, r.Body, status, code)
	}
}

func (h *harness) createChat(a account, title string) string {
	h.t.Helper()
	r := h.do("POST", "/api/v1/chats", `{"title":"`+title+`"}`, withSession(a))
	h.expect(r, http.StatusCreated, "")
	return r.JSON()["chat_id"].(string)
}

func (h *harness) join(a account, chatID, name string) cred {
	h.t.Helper()
	r := h.do("POST", "/api/v1/chats/"+chatID+"/participations", `{"display_name":"`+name+`"}`, withToken(a))
	h.expect(r, http.StatusCreated, "")
	var c cred
	if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		h.t.Fatal(err)
	}
	return c
}

// agent calls a CLI participant endpoint of the credential's chat.
func (h *harness) agent(c cred, method, sub, body string) response {
	h.t.Helper()
	return h.do(method, "/api/v1/agent/chats/"+c.ChatID+sub, body, withCred(c))
}

// web calls a web participant endpoint as the account.
func (h *harness) web(a account, chatID, method, sub, body string) response {
	h.t.Helper()
	return h.do(method, "/api/v1/web/chats/"+chatID+sub, body, withSession(a), withOrigin(origin))
}

func (h *harness) send(c cred, text, requestID string) response {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text, "client_request_id": requestID})
	return h.agent(c, "POST", "/messages", string(body))
}

// messages reads every message of the chat straight from the store.
func (h *harness) messages(chatID string) []store.Message {
	h.t.Helper()
	ms, _, err := h.store.Messages(context.Background(), chatID, 0, 0, 1000)
	if err != nil {
		h.t.Fatal(err)
	}
	return ms
}

// textMessages returns the texts of the chat's text messages.
func (h *harness) textMessages(chatID string) []string {
	var out []string
	for _, m := range h.messages(chatID) {
		if m.Kind == store.KindText {
			out = append(out, m.Text)
		}
	}
	return out
}
