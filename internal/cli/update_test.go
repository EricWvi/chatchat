package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type tarEntry struct {
	name     string
	typeflag byte
	data     string
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o755, Size: int64(len(e.data))}
		if e.typeflag == tar.TypeSymlink {
			h.Linkname, h.Size = "elsewhere", 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			tw.Write([]byte(e.data))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// releaseSite serves releases the way GitHub does: latest/download
// redirects to the newest tag.
type releaseSite struct {
	t        *testing.T
	srv      *httptest.Server
	tag      string
	archive  []byte
	manifest func(base string) releaseManifest

	mu       sync.Mutex
	requests []*http.Request
}

func newReleaseSite(t *testing.T, binary string) *releaseSite {
	s := &releaseSite{t: t, tag: "v9.9.9", archive: tarGz(t, tarEntry{"chatchat", tar.TypeReg, binary})}
	s.manifest = s.validManifest
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/releases/latest/download/latest.json":
			http.Redirect(w, r, "/releases/download/"+s.tag+"/latest.json", http.StatusFound)
		case r.URL.Path == "/releases/download/"+s.tag+"/latest.json":
			json.NewEncoder(w).Encode(s.manifest(s.base()))
		case r.URL.Path == "/releases/download/"+s.tag+"/"+s.assetName():
			w.Write(s.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *releaseSite) base() string { return s.srv.URL + "/releases" }

func (s *releaseSite) target() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (s *releaseSite) assetName() string {
	return "chatchat-" + s.tag + "-" + s.target() + ".tar.gz"
}

func (s *releaseSite) validManifest(base string) releaseManifest {
	sum := sha256.Sum256(s.archive)
	var m releaseManifest
	m.Version = s.tag
	m.Assets = append(m.Assets, struct {
		Target string `json:"target"`
		Name   string `json:"name"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}{s.target(), s.assetName(), base + "/download/" + s.tag + "/" + s.assetName(), hex.EncodeToString(sum[:])})
	return m
}

func (s *releaseSite) downloadedArchive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			return true
		}
	}
	return false
}

// update runs chatchat update against the site, replacing exe.
func (s *releaseSite) update(exe string, args ...string) result {
	var out, errOut strings.Builder
	code := Run(context.Background(), Env{
		Args: append([]string{"update"}, args...), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut,
		Getenv: func(k string) string {
			return map[string]string{serverEnv: "https://chatchat.test", tokenEnv: "cct_secret"}[k]
		},
		Version: "v1.0.0", Executable: exe, ReleaseBase: s.base(),
	})
	return result{code, out.String(), errOut.String()}
}

func installedCLI(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "chatchat")
	if err := os.WriteFile(exe, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}
	os.Chmod(exe, 0o751)
	return exe
}

func TestUpdateReplacesTheExecutable(t *testing.T) {
	site := newReleaseSite(t, "new binary")
	exe := installedCLI(t)
	r := site.update(exe, "--json")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var out map[string]string
	json.Unmarshal([]byte(r.stdout), &out)
	if out["version"] != "v9.9.9" || out["previous_version"] != "v1.0.0" || out["path"] != exe {
		t.Fatalf("stdout = %s", r.stdout)
	}
	b, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(b) != "new binary" || st.Mode().Perm() != 0o751 {
		t.Fatalf("installed %q mode %o", b, st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	// Only GitHub is contacted, and no credential goes with it.
	for _, req := range site.requests {
		if req.Header.Get("Authorization") != "" || req.Header.Get("Chatchat-Participation") != "" {
			t.Fatalf("credential sent to %s", req.URL)
		}
	}
}

func TestUpdateRejectsUntrustedReleases(t *testing.T) {
	type asset = struct {
		Target string `json:"target"`
		Name   string `json:"name"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	cases := []struct {
		name     string
		manifest func(s *releaseSite, base string) releaseManifest
		archive  []byte
		// fetched is whether the archive may be downloaded at all.
		fetched bool
	}{
		{name: "bad version", manifest: func(s *releaseSite, base string) releaseManifest {
			m := s.validManifest(base)
			m.Version = "latest"
			return m
		}},
		{name: "no asset for this platform", manifest: func(s *releaseSite, base string) releaseManifest {
			m := s.validManifest(base)
			m.Assets[0].Target = "plan9-mips"
			return m
		}},
		{name: "duplicate platform", manifest: func(s *releaseSite, base string) releaseManifest {
			m := s.validManifest(base)
			m.Assets = append(m.Assets, m.Assets[0])
			return m
		}},
		{name: "url off the release", manifest: func(s *releaseSite, base string) releaseManifest {
			m := s.validManifest(base)
			m.Assets[0].URL = "https://evil.example/" + s.assetName()
			return m
		}},
		{name: "name of another version", manifest: func(s *releaseSite, base string) releaseManifest {
			m := s.validManifest(base)
			m.Assets = []asset{{m.Assets[0].Target, "chatchat-v1.0.0-" + s.target() + ".tar.gz", m.Assets[0].URL, m.Assets[0].SHA256}}
			return m
		}},
		{name: "checksum mismatch", fetched: true, manifest: func(s *releaseSite, base string) releaseManifest {
			m := s.validManifest(base)
			m.Assets[0].SHA256 = strings.Repeat("0", 64)
			return m
		}},
		{name: "no chatchat in the archive", fetched: true, archive: tarGz(t, tarEntry{"README", tar.TypeReg, "x"})},
		{name: "chatchat is a symlink", fetched: true, archive: tarGz(t, tarEntry{"chatchat", tar.TypeSymlink, ""})},
		{name: "chatchat twice", fetched: true, archive: tarGz(t, tarEntry{"chatchat", tar.TypeReg, "a"}, tarEntry{"chatchat", tar.TypeReg, "b"})},
		{name: "not a gzip", fetched: true, archive: []byte("garbage")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			site := newReleaseSite(t, "new binary")
			if c.archive != nil {
				site.archive = c.archive
			}
			if c.manifest != nil {
				site.manifest = func(base string) releaseManifest { return c.manifest(site, base) }
			}
			exe := installedCLI(t)
			r := site.update(exe, "--json")
			var e map[string]any
			json.Unmarshal([]byte(r.stderr), &e)
			if r.code != 1 || e["code"] != "update_failed" {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			if b, _ := os.ReadFile(exe); string(b) != "old" {
				t.Fatalf("replaced with %q", b)
			}
			if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
				t.Fatalf("left behind: %v", entries)
			}
			if site.downloadedArchive() != c.fetched {
				t.Fatalf("archive downloaded = %v", !c.fetched)
			}
		})
	}
}

func TestUpdateReportsMissingRelease(t *testing.T) {
	site := newReleaseSite(t, "new binary")
	site.tag = "v0.0.0" // latest/download points nowhere
	site.srv.Config.Handler = http.NotFoundHandler()
	exe := installedCLI(t)
	if r := site.update(exe); r.code != 1 || !strings.Contains(r.stderr, "HTTP 404") {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
}

func TestExtractZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("chatchat.exe")
	w.Write([]byte("windows binary"))
	zw.Close()
	b, err := extract(buf.Bytes(), ".zip", "chatchat.exe")
	if err != nil || string(b) != "windows binary" {
		t.Fatalf("extract = %q, %v", b, err)
	}
	if _, err := extract(buf.Bytes(), ".zip", "chatchat"); err == nil {
		t.Fatal("found a missing entry")
	}
}
