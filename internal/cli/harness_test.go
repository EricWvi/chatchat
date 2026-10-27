package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"chatchat/internal/auth"
	"chatchat/internal/config"
	"chatchat/internal/server"
	"chatchat/internal/store"
)

// harness is a real chatchat server and a home directory for the CLI.
type harness struct {
	t     *testing.T
	store *store.Store
	srv   *httptest.Server
	home  string
	// handler serves the API; tests may wrap it to inject faults.
	handler http.Handler
	user    store.User
	token   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	authn := auth.New(st, auth.Config{PublicURL: "https://chatchat.test", Issuer: "https://idp.invalid", ClientID: "x", ClientSecret: "y"})
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	srv, err := server.New(t.Context(), st, authn, web, server.Options{
		Version: "test", PublicURL: "https://chatchat.test", Limits: config.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	h := &harness{t: t, store: st, home: t.TempDir(), handler: srv}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.handler.ServeHTTP(w, r) }))
	t.Cleanup(h.srv.Close)

	ctx := context.Background()
	h.user, err = st.UpsertUser(ctx, store.Identity{Issuer: "https://idp.test", Subject: "alice", Name: "Alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, h.token, err = st.CreateUserToken(ctx, h.user.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) createChat(title string) string {
	h.t.Helper()
	c, err := h.store.CreateChat(context.Background(), h.user.ID, title, "", 24*time.Hour)
	if err != nil {
		h.t.Fatal(err)
	}
	return c.ID
}

// result is the outcome of one CLI run.
type result struct {
	code           int
	stdout, stderr string
}

// run runs the CLI with the harness's home, server and user token in the
// environment, plus env (KEY=VALUE, an empty value unsets).
func (h *harness) run(stdin string, env []string, args ...string) result {
	h.t.Helper()
	vars := map[string]string{
		"HOME":            h.home,
		"XDG_CONFIG_HOME": "",
		"XDG_STATE_HOME":  "",
		serverEnv:         h.srv.URL,
		tokenEnv:          h.token,
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	var out, errOut strings.Builder
	code := Run(context.Background(), Env{
		Args: args, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut,
		Getenv:          func(k string) string { return vars[k] },
		Version:         "test",
		SendRetryDelays: []time.Duration{0, 0},
	})
	return result{code, out.String(), errOut.String()}
}

// expect fails unless the run exited with code.
func (h *harness) expect(r result, code int) {
	h.t.Helper()
	if r.code != code {
		h.t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
}

// join runs chatchat join and returns the credential line.
func (h *harness) join(chatID, name string) (string, credential) {
	h.t.Helper()
	r := h.run("", nil, "join", chatID, "--name", name)
	h.expect(r, 0)
	line := strings.TrimSpace(r.stdout)
	var c credential
	if err := json.Unmarshal([]byte(line), &c); err != nil {
		h.t.Fatalf("join printed %q: %v", r.stdout, err)
	}
	return line, c
}

// localFiles returns the contents of every file the CLI wrote under the
// home directory.
func (h *harness) localFiles() map[string]string {
	h.t.Helper()
	files := map[string]string{}
	filepath.WalkDir(h.home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			h.t.Fatal(err)
		}
		files[path] = string(b)
		return nil
	})
	return files
}

func (h *harness) historyFile() string {
	return filepath.Join(h.home, ".local", "state", "chatchat", "history.jsonl")
}

func (h *harness) configFile() string {
	return filepath.Join(h.home, ".config", "chatchat", "config.yaml")
}
