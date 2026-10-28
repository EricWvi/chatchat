package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *app) cmdConfig(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: chatchat config check | set-token")
	}
	switch args[0] {
	case "check":
		return a.cmdConfigCheck(args[1:])
	case "set-token":
		return a.cmdSetToken(args[1:])
	}
	return usageErr("unknown config subcommand %q; use check or set-token", args[0])
}

// cmdSetToken saves the user token from stdin, and the server when
// --server is given, to the config file.
func (a *app) cmdSetToken(args []string) error {
	fs := a.flags("config set-token", "< token (read from the first line of stdin)")
	if pos, err := parse(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return usageErr("the user token is read from stdin only; do not put it in command line arguments")
	}
	if isTerminal(a.env.Stdin) {
		fmt.Fprintln(a.env.Stderr, "Paste the user token and press Enter:")
	}
	line, err := a.readLine()
	if err != nil {
		return err
	}
	token := strings.TrimSpace(line)
	switch {
	case token == "":
		return usageErr("the first line of stdin is empty; no user token")
	case strings.HasPrefix(token, participationSecretPrefix):
		return &cliError{exit: exitUsage, code: "wrong_credential_type", msg: "this is a participation secret, not a user token", hint: "user tokens start with cct_; create one on the website's user token page"}
	case !strings.HasPrefix(token, userTokenPrefix):
		return usageErr("a user token starts with cct_")
	}
	path, err := a.configPath()
	if err != nil {
		return err
	}
	c, err := loadConfig(path)
	if err != nil {
		return err
	}
	c.Token = token
	if a.server != "" {
		if c.Server, err = normalizeServer(a.server); err != nil {
			return err
		}
	}
	if err := saveConfig(path, c); err != nil {
		return fmt.Errorf("could not save the config file: %w", err)
	}
	if a.json {
		writeJSONLine(a.env.Stdout, map[string]any{"config_path": path, "server": nullable(c.Server)})
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "Saved to %s\n", path)
	if c.Server == "" {
		fmt.Fprintln(a.env.Stdout, "No server address yet: run set-token again with --server URL, or set "+serverEnv)
	}
	fmt.Fprintln(a.env.Stdout, "Run chatchat config check to verify")
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// cmdConfigCheck checks the config file, the server, the protocol and the
// user token, reporting every step; it fails with the first problem.
func (a *app) cmdConfigCheck(args []string) error {
	fs := a.flags("config check", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	report := map[string]any{"ok": false}
	var lines []string
	say := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	finish := func(err error) error {
		if a.json {
			report["ok"] = err == nil
			writeJSONLine(a.env.Stdout, report)
		} else {
			for _, l := range lines {
				fmt.Fprintln(a.env.Stdout, l)
			}
		}
		return err
	}

	s, err := a.settings()
	if s.configPath != "" {
		report["config_path"] = s.configPath
		if st, statErr := os.Stat(s.configPath); statErr == nil {
			say("✓ config file %s", s.configPath)
			if st.Mode().Perm()&0o077 != 0 {
				say("! config file mode is %o; change it to 600: chmod 600 %s", st.Mode().Perm(), s.configPath)
				report["config_mode"] = fmt.Sprintf("%o", st.Mode().Perm())
			}
		} else {
			say("- config file %s does not exist", s.configPath)
		}
	}
	if err != nil {
		say("✗ %v", err)
		return finish(err)
	}

	server, err := s.requireServer()
	if err != nil {
		say("✗ %v", err)
		return finish(err)
	}
	report["server"], report["server_source"] = server, s.serverSource
	say("✓ server %s (from %s)", server, s.serverSource)

	token, tokenErr := s.requireToken()
	cl, m, err := a.connect(server, bearerAuth(token))
	if err != nil {
		say("✗ %v", err)
		return finish(err)
	}
	report["protocol_version"], report["server_version"] = m.ProtocolVersion, m.ServerVersion
	say("✓ protocol version %d is compatible (server %s)", m.ProtocolVersion, m.ServerVersion)

	if tokenErr != nil {
		say("✗ %v", tokenErr)
		return finish(tokenErr)
	}
	report["token_source"] = s.tokenSource
	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if _, _, err := cl.call(a.ctx, "GET", "/api/v1/me", nil, nil, &me, requestTimeout); err != nil {
		say("✗ user token (from %s): %v", s.tokenSource, err)
		return finish(err)
	}
	report["user"] = me
	who := me.Name
	if me.Email != "" {
		who += " <" + me.Email + ">"
	}
	say("✓ user token is valid (from %s), user %s", s.tokenSource, who)
	return finish(nil)
}

// chatIDPattern matches the opaque chat IDs the server issues.
var chatIDPattern = regexp.MustCompile(`^c_[A-Za-z0-9_-]+$`)

// parseChatRef takes a chat ID or a chat link (…/c/<chat_id>).
func parseChatRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "/") {
		if u, err := url.Parse(ref); err == nil {
			ref = u.Path
		}
		ref = strings.TrimRight(ref, "/")
		ref = ref[strings.LastIndex(ref, "/")+1:]
	}
	if !chatIDPattern.MatchString(ref) {
		return "", usageErr("not a valid chat ID or chat link")
	}
	return ref, nil
}

func (a *app) cmdJoin(args []string) error {
	fs := a.flags("join", "<chat_id or link> --name <display name>")
	name := fs.String("name", "", "display name (required), e.g. \"Claude (API review)\"")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("usage: chatchat join <chat_id or link> --name <display name>")
	}
	if strings.TrimSpace(*name) == "" {
		return usageErr("missing --name: give this participant a display name")
	}
	chatID, err := parseChatRef(pos[0])
	if err != nil {
		return err
	}
	s, err := a.settings()
	if err != nil {
		return err
	}
	server, err := s.requireServer()
	if err != nil {
		return err
	}
	token, err := s.requireToken()
	if err != nil {
		return err
	}
	cl, _, err := a.connect(server, bearerAuth(token))
	if err != nil {
		return err
	}
	var resp struct {
		credential
		JoinedAt string `json:"joined_at"`
	}
	if _, _, err := cl.call(a.ctx, "POST", apiPath("/api/v1/chats", chatID, "participations"), nil,
		map[string]string{"display_name": *name}, &resp, requestTimeout); err != nil {
		return err
	}
	cred := resp.credential
	cred.Server = server
	// The credential line is the output, with or without --json.
	writeJSONLine(a.env.Stdout, cred)

	if path, err := a.historyPath(); err != nil {
		fmt.Fprintf(a.env.Stderr, "chatchat: local history not written: %v\n", err)
	} else if err := appendHistory(path, historyEntry{
		Server: server, ChatID: cred.ChatID, ParticipationID: cred.ParticipationID,
		DisplayName: cred.DisplayName, JoinedAt: resp.JoinedAt,
	}); err != nil {
		fmt.Fprintf(a.env.Stderr, "chatchat: local history not written: %v\n", err)
	}
	if !a.json {
		fmt.Fprintln(a.env.Stderr, "Joined. The line above is the credential line and is shown only once: pass it as the first line of stdin to later commands, and never post it in the chat.")
	}
	return nil
}

func (a *app) cmdState(args []string) error {
	fs := a.flags("state", "< credential line")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, c, err := a.participantClient()
	if err != nil {
		return err
	}
	var st stateView
	_, raw, err := cl.call(a.ctx, "GET", apiPath("/api/v1/agent/chats", c.ChatID, "state"), nil, nil, &st, requestTimeout)
	if err != nil {
		return err
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	printState(a.env.Stdout, st)
	return nil
}

func (a *app) cmdRead(args []string) error {
	fs := a.flags("read", "[--after N] [--limit N] < credential line")
	after := fs.Int64("after", 0, "read messages with sequence greater than N")
	limit := fs.Int("limit", 0, "maximum number of messages (server default if unset)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *after < 0 || *limit < 0 {
		return usageErr("--after and --limit must not be negative")
	}
	cl, c, err := a.participantClient()
	if err != nil {
		return err
	}
	q := url.Values{"after_sequence": {strconv.FormatInt(*after, 10)}}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	var page messagePage
	_, raw, err := cl.call(a.ctx, "GET", apiPath("/api/v1/agent/chats", c.ChatID, "messages"), q, nil, &page, requestTimeout)
	if err != nil {
		return err
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	printPage(a.env.Stdout, page, *after, false)
	return nil
}

// cmdWait waits for messages after --after. A timeout longer than the
// server's cap on a single wait is covered by consecutive waits, each of
// which renews the lease.
func (a *app) cmdWait(args []string) error {
	fs := a.flags("wait", "--after N [--timeout S] < credential line")
	after := fs.Int64("after", 0, "wait for messages with sequence greater than N (required)")
	timeout := fs.Int("timeout", -1, "seconds to wait at most (defaults to the server's single-wait cap; 0 checks once)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !visited(fs, "after") {
		return usageErr("missing --after: pass the last sequence you have read")
	}
	if *after < 0 {
		return usageErr("--after must not be negative")
	}
	cl, c, err := a.participantClient()
	if err != nil {
		return err
	}
	total := cl.waitMax
	if *timeout >= 0 {
		total = time.Duration(*timeout) * time.Second
	}
	deadline := time.Now().Add(total)
	path := apiPath("/api/v1/agent/chats", c.ChatID, "messages", "wait")
	for {
		wait := min(max(time.Until(deadline), 0), cl.waitMax)
		secs := int((wait + time.Second - 1) / time.Second)
		q := url.Values{"after_sequence": {strconv.FormatInt(*after, 10)}, "timeout_seconds": {strconv.Itoa(secs)}}
		var page messagePage
		_, raw, err := cl.call(a.ctx, "GET", path, q, nil, &page, waitTimeout(time.Duration(secs)*time.Second))
		if err != nil {
			return err
		}
		if len(page.Messages) > 0 || !time.Now().Before(deadline) {
			if a.json {
				a.printRaw(raw)
			} else {
				printPage(a.env.Stdout, page, *after, true)
			}
			return nil
		}
	}
}

// newRequestID returns a random client_request_id.
func newRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *app) cmdSend(args []string) error {
	fs := a.flags("send", "[--text TEXT] [--request-id ID] < credential line (message text from the second line)")
	text := fs.String("text", "", "message text; read from the second line of stdin when not given")
	requestID := fs.String("request-id", "", "client_request_id; when retrying a failed send, pass the value printed last time")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if visited(fs, "request-id") && (*requestID == "" || utf8.RuneCountInString(*requestID) > 64) {
		return usageErr("--request-id must be 1-64 characters")
	}
	c, err := a.readCredential()
	if err != nil {
		return err
	}
	rest, err := io.ReadAll(a.stdin)
	if err != nil {
		return fmt.Errorf("could not read stdin: %w", err)
	}
	body := strings.TrimRight(string(rest), "\r\n")
	if visited(fs, "text") {
		if strings.TrimSpace(body) != "" {
			return usageErr("give the text either with --text or from the second line of stdin, not both")
		}
		body = *text
	}
	if strings.TrimSpace(body) == "" {
		return usageErr("the message text is empty: use --text or write it from the second line of stdin")
	}
	id := *requestID
	if id == "" {
		id = newRequestID()
	}
	server, err := a.participantServer(c)
	if err != nil {
		return err
	}
	cl, _, err := a.connect(server, participantAuth(c))
	if err != nil {
		return withRequestID(err, id)
	}

	path := apiPath("/api/v1/agent/chats", c.ChatID, "messages")
	req := map[string]string{"text": body, "client_request_id": id}
	var (
		status int
		raw    []byte
	)
	for attempt := 0; ; attempt++ {
		status, raw, err = cl.roundTrip(a.ctx, "POST", path, nil, req, requestTimeout)
		if err == nil || !retryable(err) || attempt == len(a.env.SendRetryDelays) {
			break
		}
		select {
		case <-time.After(a.env.SendRetryDelays[attempt]):
		case <-a.ctx.Done():
			return withRequestID(classify(err), id)
		}
	}
	if err != nil {
		return withRequestID(classify(err), id)
	}
	var m messageView
	if err := decodeJSON(raw, &m); err != nil {
		return withRequestID(err, id)
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	note := ""
	if status == 200 {
		note = " (duplicate request; returned the existing message)"
	}
	fmt.Fprintf(a.env.Stdout, "Sent #%d%s\n", m.Sequence, note)
	return nil
}

func withRequestID(err error, id string) error {
	var ce *cliError
	if errors.As(err, &ce) {
		ce.requestID = id
		return ce
	}
	return &cliError{exit: exitError, code: "error", msg: err.Error(), requestID: id}
}

func (a *app) cmdProposeEnd(args []string) error {
	fs := a.flags("propose-end", "[--reason REASON] < credential line")
	reason := fs.String("reason", "", "why to end the chat (optional, at most 500 characters)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, c, err := a.participantClient()
	if err != nil {
		return err
	}
	var m messageView
	_, raw, err := cl.call(a.ctx, "POST", apiPath("/api/v1/agent/chats", c.ChatID, "propose-end"), nil,
		map[string]string{"reason": *reason}, &m, requestTimeout)
	if err != nil {
		return err
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "Proposed ending in #%d; the creator decides whether to destroy the chat\n", m.Sequence)
	return nil
}

func (a *app) cmdLeave(args []string) error {
	fs := a.flags("leave", "< credential line")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, c, err := a.participantClient()
	if err != nil {
		return err
	}
	var resp struct {
		ParticipationID string `json:"participation_id"`
		LeftAt          string `json:"left_at"`
	}
	_, raw, err := cl.call(a.ctx, "POST", apiPath("/api/v1/agent/chats", c.ChatID, "leave"), nil, nil, &resp, requestTimeout)
	if err != nil {
		return err
	}
	if path, err := a.historyPath(); err == nil {
		if err := markLeft(path, c.ParticipationID, resp.LeftAt); err != nil {
			fmt.Fprintf(a.env.Stderr, "chatchat: local history not updated: %v\n", err)
		}
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "Left as %s; these credentials no longer work\n", c.ParticipationID)
	return nil
}

func (a *app) cmdHistory(args []string) error {
	fs := a.flags("history", "[--remote]")
	remote := fs.Bool("remote", false, "list participations recorded on the server (needs the user token)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *remote {
		return a.remoteHistory()
	}
	path, err := a.historyPath()
	if err != nil {
		return err
	}
	entries, err := readHistory(path)
	if err != nil {
		return fmt.Errorf("could not read the local history: %w", err)
	}
	if a.json {
		if entries == nil {
			entries = []historyEntry{}
		}
		writeJSONLine(a.env.Stdout, map[string]any{"participations": entries})
		return nil
	}
	if len(entries) == 0 {
		fmt.Fprintln(a.env.Stdout, "No local participations")
		return nil
	}
	for _, e := range entries {
		left := "not left"
		if e.LeftAt != nil {
			left = "left " + localTime(*e.LeftAt)
		}
		fmt.Fprintf(a.env.Stdout, "%s  %s  %s  %s  joined %s  %s\n",
			e.ChatID, e.ParticipationID, e.DisplayName, e.Server, localTime(e.JoinedAt), left)
	}
	fmt.Fprintln(a.env.Stdout, "History is for lookup and troubleshooting only and cannot restore an identity; join again to keep taking part")
	return nil
}

func (a *app) remoteHistory() error {
	s, err := a.settings()
	if err != nil {
		return err
	}
	server, err := s.requireServer()
	if err != nil {
		return err
	}
	token, err := s.requireToken()
	if err != nil {
		return err
	}
	cl, _, err := a.connect(server, bearerAuth(token))
	if err != nil {
		return err
	}
	var resp struct {
		Participations []struct {
			ParticipationID string  `json:"participation_id"`
			ChatID          string  `json:"chat_id"`
			Kind            string  `json:"kind"`
			DisplayName     string  `json:"display_name"`
			JoinedAt        string  `json:"joined_at"`
			LeftAt          *string `json:"left_at"`
			EndReason       *string `json:"end_reason"`
		} `json:"participations"`
	}
	_, raw, err := cl.call(a.ctx, "GET", "/api/v1/participations", nil, nil, &resp, requestTimeout)
	if err != nil {
		return err
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	if len(resp.Participations) == 0 {
		fmt.Fprintln(a.env.Stdout, "The server has no participations of yours")
		return nil
	}
	for _, p := range resp.Participations {
		end := "active"
		if p.EndReason != nil {
			end = map[string]string{"left": "left", "chat_destroyed": "chat destroyed"}[*p.EndReason]
			if p.LeftAt != nil {
				end += " (" + localTime(*p.LeftAt) + ")"
			}
		}
		fmt.Fprintf(a.env.Stdout, "%s  %s  %s  %s  joined %s  %s\n",
			p.ChatID, p.ParticipationID, kindName(p.Kind), p.DisplayName, localTime(p.JoinedAt), end)
	}
	return nil
}
