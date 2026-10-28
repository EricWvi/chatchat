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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// DefaultReleaseBase is where CLI releases are published. latest.json is
// at <base>/latest/download/latest.json and every asset it lists must be
// at <base>/download/<tag>/<name>.
const DefaultReleaseBase = "https://github.com/EricWvi/chatchat/releases"

const (
	updateTimeout    = 5 * time.Minute
	maxManifestBytes = 1 << 20
	maxArchiveBytes  = 64 << 20
)

// releaseManifest is latest.json, written by scripts/cli-release-manifest.mjs.
type releaseManifest struct {
	Version string `json:"version"`
	Assets  []struct {
		Target string `json:"target"`
		Name   string `json:"name"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"assets"`
}

var (
	releaseTagPattern = regexp.MustCompile(`^v[\w.-]+$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func updateErr(format string, args ...any) error {
	return &cliError{exit: exitError, code: "update_failed", msg: fmt.Sprintf(format, args...)}
}

// cmdUpdate installs the latest release over the running executable. It
// reads no config and sends no credential: only GitHub is contacted.
func (a *app) cmdUpdate(args []string) error {
	fs := a.flags("update", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	exe := a.env.Executable
	if exe == "" {
		p, err := os.Executable()
		if err != nil {
			return updateErr("cannot find the path of this program: %v", err)
		}
		if exe, err = filepath.EvalSymlinks(p); err != nil {
			return updateErr("cannot find the path of this program: %v", err)
		}
	}
	base := strings.TrimSuffix(a.env.ReleaseBase, "/")
	if base == "" {
		base = DefaultReleaseBase
	}
	ctx, cancel := context.WithTimeout(a.ctx, updateTimeout)
	defer cancel()

	version, binary, err := a.fetchRelease(ctx, base, runtime.GOOS+"-"+runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := replaceExecutable(exe, binary); err != nil {
		return updateErr("could not replace %s: %v", exe, err)
	}
	if a.json {
		writeJSONLine(a.env.Stdout, map[string]any{"version": version, "previous_version": a.env.Version, "path": exe})
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "Updated to %s (%s)\n", version, exe)
	return nil
}

// fetchRelease downloads the latest release for target and returns its
// tag and the verified executable.
func (a *app) fetchRelease(ctx context.Context, base, target string) (string, []byte, error) {
	raw, err := a.download(ctx, base+"/latest/download/latest.json", maxManifestBytes)
	if err != nil {
		return "", nil, err
	}
	var m releaseManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", nil, updateErr("could not parse latest.json")
	}
	if !releaseTagPattern.MatchString(m.Version) {
		return "", nil, updateErr("latest.json has an invalid version")
	}
	ext := ".tar.gz"
	if strings.HasPrefix(target, "windows-") {
		ext = ".zip"
	}
	name := "chatchat-" + m.Version + "-" + target + ext
	found := -1
	for i, as := range m.Assets {
		if as.Target != target {
			continue
		}
		if found >= 0 {
			return "", nil, updateErr("latest.json lists target %s more than once", target)
		}
		found = i
	}
	if found < 0 {
		return "", nil, updateErr("the latest release %s has no package for %s", m.Version, target)
	}
	as := m.Assets[found]
	if as.Name != name || as.URL != base+"/download/"+m.Version+"/"+name || !sha256Pattern.MatchString(as.SHA256) {
		return "", nil, updateErr("the latest.json entry for %s does not match version %s", target, m.Version)
	}
	archive, err := a.download(ctx, as.URL, maxArchiveBytes)
	if err != nil {
		return "", nil, err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != as.SHA256 {
		return "", nil, updateErr("the SHA-256 of %s does not match latest.json; nothing installed", name)
	}
	exe := "chatchat"
	if ext == ".zip" {
		exe = "chatchat.exe"
	}
	binary, err := extract(archive, ext, exe)
	if err != nil {
		return "", nil, updateErr("%s is not a valid release package: %v", name, err)
	}
	return m.Version, binary, nil
}

func (a *app) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, updateErr("invalid download URL")
	}
	req.Header.Set("User-Agent", "chatchat/"+a.env.Version)
	resp, err := a.env.HTTP.Do(req)
	if err != nil {
		return nil, &cliError{exit: exitError, code: "network", msg: "download failed: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, updateErr("could not download %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, &cliError{exit: exitError, code: "network", msg: "download failed: " + err.Error()}
	}
	if int64(len(body)) > limit {
		return nil, updateErr("could not download %s: file too large", url)
	}
	return body, nil
}

// extract returns the regular file called name at the top of the archive.
func extract(archive []byte, ext, name string) ([]byte, error) {
	var out []byte
	found := false
	add := func(entry string, regular bool, r io.Reader) error {
		if entry != name {
			return nil
		}
		if found {
			return errors.New("the package holds more than one " + name)
		}
		if !regular {
			return errors.New(name + " is not a regular file")
		}
		b, err := io.ReadAll(io.LimitReader(r, maxArchiveBytes+1))
		if err != nil {
			return err
		}
		if len(b) == 0 || len(b) > maxArchiveBytes {
			return errors.New(name + " has the wrong size")
		}
		out, found = b, true
		return nil
	}
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = add(f.Name, f.Mode().IsRegular(), rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if err := add(h.Name, h.Typeflag == tar.TypeReg, tr); err != nil {
				return nil, err
			}
		}
	}
	if !found {
		return nil, errors.New("the package has no " + name)
	}
	return out, nil
}

// replaceExecutable atomically puts binary at path with path's current
// permissions. The running program keeps its open file on Unix; Windows
// cannot overwrite a running executable, so the old one is moved aside
// first and removed by the next update.
func replaceExecutable(path string, binary []byte) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	old := path + ".old"
	os.Remove(old)
	tmp, err := os.CreateTemp(dir, ".chatchat-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), st.Mode().Perm()); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.Rename(path, old); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Rename(old, path)
			return err
		}
		return nil
	}
	return os.Rename(tmp.Name(), path)
}
