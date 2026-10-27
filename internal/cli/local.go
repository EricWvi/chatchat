package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment variables that override the config file.
const (
	serverEnv = "CHATCHAT_SERVER"
	tokenEnv  = "CHATCHAT_TOKEN"
)

// Credential prefixes, as the server issues them.
const (
	userTokenPrefix           = "cct_"
	participationSecretPrefix = "ccp_"
)

// fileConfig is the config file: the server and the user token, nothing
// else.
type fileConfig struct {
	Server string `yaml:"server,omitempty"`
	Token  string `yaml:"token,omitempty"`
}

// xdgDir returns $<env>, or $HOME/<fallback> when it is unset.
func (a *app) xdgDir(env, fallback string) (string, error) {
	if d := a.env.Getenv(env); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "chatchat"), nil
	}
	home := a.env.Getenv("HOME")
	if home == "" {
		return "", &cliError{exit: exitError, code: "no_home", msg: fmt.Sprintf("HOME 和 %s 都没有设置，找不到本地文件目录", env)}
	}
	return filepath.Join(home, fallback, "chatchat"), nil
}

func (a *app) configPath() (string, error) {
	dir, err := a.xdgDir("XDG_CONFIG_HOME", ".config")
	return filepath.Join(dir, "config.yaml"), err
}

func (a *app) historyPath() (string, error) {
	dir, err := a.xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	return filepath.Join(dir, "history.jsonl"), err
}

// loadConfig reads the config file; a missing file is an empty config.
func loadConfig(path string) (fileConfig, error) {
	var c fileConfig
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, &cliError{exit: exitError, code: "config", msg: "读取配置文件失败：" + err.Error()}
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		// The YAML error may quote the file, which holds the token.
		return c, &cliError{exit: exitError, code: "config", msg: "配置文件 " + path + " 不是有效的 YAML"}
	}
	return c, nil
}

// saveConfig writes the config file with mode 0600.
func saveConfig(path string, c fileConfig) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// writeFileAtomic replaces path with data, readable only by the owner.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// settings are the server and user token in effect, and where each came
// from.
type settings struct {
	configPath   string
	server       string
	serverSource string
	token        string
	tokenSource  string
}

// settings resolves the server (--server, then CHATCHAT_SERVER, then the
// config file) and the user token (CHATCHAT_TOKEN, then the config file).
func (a *app) settings() (settings, error) {
	var s settings
	path, err := a.configPath()
	if err != nil {
		return s, err
	}
	s.configPath = path
	c, err := loadConfig(path)
	if err != nil {
		return s, err
	}
	switch {
	case a.server != "":
		s.server, s.serverSource = a.server, "--server"
	case a.env.Getenv(serverEnv) != "":
		s.server, s.serverSource = a.env.Getenv(serverEnv), serverEnv
	case c.Server != "":
		s.server, s.serverSource = c.Server, path
	}
	switch {
	case a.env.Getenv(tokenEnv) != "":
		s.token, s.tokenSource = a.env.Getenv(tokenEnv), tokenEnv
	case c.Token != "":
		s.token, s.tokenSource = c.Token, path
	}
	return s, nil
}

// requireServer returns the normalized server address.
func (s settings) requireServer() (string, error) {
	if s.server == "" {
		return "", &cliError{
			exit: exitError, code: "no_server", msg: "没有配置服务端地址",
			hint: fmt.Sprintf("用 --server、%s 指定，或写进 %s 的 server 字段", serverEnv, s.configPath),
		}
	}
	return normalizeServer(s.server)
}

// requireToken returns the user token.
func (s settings) requireToken() (string, error) {
	if s.token == "" {
		return "", &cliError{
			exit: exitCredentials, code: "no_token", msg: "没有配置用户 token",
			hint: "在网站生成用户 token，用 chatchat config set-token 保存，或设置 " + tokenEnv,
		}
	}
	return s.token, nil
}

// normalizeServer checks a server address and drops its trailing slash.
func normalizeServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", &cliError{exit: exitUsage, code: "usage", msg: "服务端地址必须是 http(s)://host[:port] 形式的 URL"}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// credential is the line join prints and the participant commands read
// from the first line of stdin.
type credential struct {
	Server          string `json:"server"`
	ChatID          string `json:"chat_id"`
	ParticipationID string `json:"participation_id"`
	Secret          string `json:"participation_secret"`
	DisplayName     string `json:"display_name"`
}

func credentialErr(msg string) error {
	return &cliError{
		exit: exitCredentials, code: "invalid_credential_line", msg: msg,
		hint: "把 join 输出的凭据行原样作为 stdin 第一行传入",
	}
}

// isTerminal reports whether r is an interactive terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&fs.ModeCharDevice != 0
}

// readLine reads the first line of stdin without its line ending.
func (a *app) readLine() (string, error) {
	line, err := a.stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readCredential reads the credential line from stdin. Errors never quote
// the line: it holds the secret.
func (a *app) readCredential() (credential, error) {
	var c credential
	if isTerminal(a.env.Stdin) {
		return c, credentialErr("需要从 stdin 第一行传入凭据行，不能从终端交互输入")
	}
	line, err := a.readLine()
	if err != nil {
		return c, credentialErr("读取 stdin 失败")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return c, credentialErr("stdin 第一行是空的，没有凭据行")
	}
	dec := json.NewDecoder(strings.NewReader(line))
	if err := dec.Decode(&c); err != nil {
		return c, credentialErr("stdin 第一行不是有效的凭据 JSON")
	}
	switch {
	case c.ChatID == "" || c.ParticipationID == "" || c.Secret == "":
		return c, credentialErr("凭据行缺少 chat_id、participation_id 或 participation_secret")
	case strings.HasPrefix(c.Secret, userTokenPrefix):
		return c, &cliError{
			exit: exitCredentials, code: "wrong_credential_type", msg: "participation_secret 填的是用户 token",
			hint: "参与者命令用 join 输出的凭据行，不能用用户 token",
		}
	}
	return c, nil
}

// participantServer is the server a credential belongs to: the one in the
// credential line, which issued it, or else the configured one.
func (a *app) participantServer(c credential) (string, error) {
	if c.Server != "" {
		return normalizeServer(c.Server)
	}
	s, err := a.settings()
	if err != nil {
		return "", err
	}
	return s.requireServer()
}

// participantClient connects as the participant of the credential line on
// stdin.
func (a *app) participantClient() (*client, credential, error) {
	c, err := a.readCredential()
	if err != nil {
		return nil, c, err
	}
	server, err := a.participantServer(c)
	if err != nil {
		return nil, c, err
	}
	cl, _, err := a.connect(server, participantAuth(c))
	return cl, c, err
}

// participantAuth sets the headers of the participant endpoints.
func participantAuth(c credential) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+c.Secret)
		r.Header.Set("Chatchat-Participation", c.ParticipationID)
	}
}

// historyEntry is one participation in the local history: metadata only.
type historyEntry struct {
	Server          string  `json:"server"`
	ChatID          string  `json:"chat_id"`
	ParticipationID string  `json:"participation_id"`
	DisplayName     string  `json:"display_name"`
	JoinedAt        string  `json:"joined_at"`
	LeftAt          *string `json:"left_at"`
}

// withHistoryLock runs fn holding the history lock, so concurrent runs
// from several agents do not lose each other's entries.
func withHistoryLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return err
	}
	return fn()
}

// appendHistory records a new participation.
func appendHistory(path string, e historyEntry) error {
	return withHistoryLock(path, func() error {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		writeJSONLine(&buf, e)
		if _, err := f.Write(buf.Bytes()); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	})
}

// markLeft fills in left_at for a participation. A participation joined
// elsewhere has no entry and is left alone.
func markLeft(path, participationID, leftAt string) error {
	return withHistoryLock(path, func() error {
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		var out bytes.Buffer
		changed := false
		for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
			var e historyEntry
			if json.Unmarshal(line, &e) == nil && e.ParticipationID == participationID && e.LeftAt == nil {
				e.LeftAt = &leftAt
				writeJSONLine(&out, e)
				changed = true
				continue
			}
			out.Write(line)
		}
		if !changed {
			return nil
		}
		return writeFileAtomic(path, out.Bytes())
	})
}

// readHistory returns the entries of the history file, oldest first.
// Lines that do not parse are skipped.
func readHistory(path string) ([]historyEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []historyEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e historyEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ParticipationID != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
