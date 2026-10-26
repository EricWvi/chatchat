package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

type staticFile struct {
	data        []byte
	etag        string
	contentType string
}

// static serves the built frontend from memory.
//
// Vite emits content-hashed files under assets/, which are cached forever.
// Everything else (index.html, favicon) must be revalidated so a new
// release is picked up.
type static struct {
	files map[string]staticFile
}

func newStatic(web fs.FS) (*static, error) {
	s := &static{files: map[string]staticFile{}}
	err := fs.WalkDir(web, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			return err
		}
		data, err := fs.ReadFile(web, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		s.files[name] = staticFile{
			data:        data,
			etag:        `"` + hex.EncodeToString(sum[:8]) + `"`,
			contentType: ct,
		}
		return nil
	})
	return s, err
}

func (s *static) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	f, ok := s.files[name]
	if !ok {
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		// Unknown page paths render the app shell.
		f, ok = s.files["index.html"]
		if !ok {
			http.Error(w, "frontend not built; run task build:frontend", http.StatusServiceUnavailable)
			return
		}
	}
	h := w.Header()
	if strings.HasPrefix(name, "assets/") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Type", f.contentType)
	h.Set("ETag", f.etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
}
