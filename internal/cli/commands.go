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
		return usageErr("用法：chatchat config check | set-token")
	}
	switch args[0] {
	case "check":
		return a.cmdConfigCheck(args[1:])
	case "set-token":
		return a.cmdSetToken(args[1:])
	}
	return usageErr("未知的 config 子命令 %q，可用：check、set-token", args[0])
}

// cmdSetToken saves the user token from stdin, and the server when
// --server is given, to the config file.
func (a *app) cmdSetToken(args []string) error {
	fs := a.flags("config set-token", "< token（从 stdin 第一行读取）")
	if pos, err := parse(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return usageErr("用户 token 只从 stdin 读取，不要写在命令行参数里")
	}
	if isTerminal(a.env.Stdin) {
		fmt.Fprintln(a.env.Stderr, "粘贴用户 token 后回车：")
	}
	line, err := a.readLine()
	if err != nil {
		return err
	}
	token := strings.TrimSpace(line)
	switch {
	case token == "":
		return usageErr("stdin 第一行是空的，没有用户 token")
	case strings.HasPrefix(token, participationSecretPrefix):
		return &cliError{exit: exitUsage, code: "wrong_credential_type", msg: "这是参与凭据，不是用户 token", hint: "用户 token 以 cct_ 开头，在网站的「用户 token」页生成"}
	case !strings.HasPrefix(token, userTokenPrefix):
		return usageErr("用户 token 应以 cct_ 开头")
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
		return fmt.Errorf("保存配置文件失败：%w", err)
	}
	if a.json {
		writeJSONLine(a.env.Stdout, map[string]any{"config_path": path, "server": nullable(c.Server)})
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "已保存到 %s\n", path)
	if c.Server == "" {
		fmt.Fprintln(a.env.Stdout, "还没有服务端地址：用 --server URL 再运行一次 set-token，或设置 "+serverEnv)
	}
	fmt.Fprintln(a.env.Stdout, "运行 chatchat config check 检查")
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
			say("✓ 配置文件 %s", s.configPath)
			if st.Mode().Perm()&0o077 != 0 {
				say("! 配置文件权限是 %o，建议改成 600：chmod 600 %s", st.Mode().Perm(), s.configPath)
				report["config_mode"] = fmt.Sprintf("%o", st.Mode().Perm())
			}
		} else {
			say("- 配置文件 %s 不存在", s.configPath)
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
	say("✓ 服务端 %s（来自 %s）", server, s.serverSource)

	token, tokenErr := s.requireToken()
	cl, m, err := a.connect(server, bearerAuth(token))
	if err != nil {
		say("✗ %v", err)
		return finish(err)
	}
	report["protocol_version"], report["server_version"] = m.ProtocolVersion, m.ServerVersion
	say("✓ 协议版本 %d 兼容（服务端 %s）", m.ProtocolVersion, m.ServerVersion)

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
		say("✗ 用户 token（来自 %s）：%v", s.tokenSource, err)
		return finish(err)
	}
	report["user"] = me
	who := me.Name
	if me.Email != "" {
		who += " <" + me.Email + ">"
	}
	say("✓ 用户 token 有效（来自 %s），用户 %s", s.tokenSource, who)
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
		return "", usageErr("不是有效的 chat ID 或 chat 链接")
	}
	return ref, nil
}

func (a *app) cmdJoin(args []string) error {
	fs := a.flags("join", "<chat_id 或链接> --name <显示名>")
	name := fs.String("name", "", "显示名（必填），例如 \"Claude (重构讨论)\"")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("用法：chatchat join <chat_id 或链接> --name <显示名>")
	}
	if strings.TrimSpace(*name) == "" {
		return usageErr("缺少 --name：给这个参与者起一个显示名")
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
		fmt.Fprintf(a.env.Stderr, "chatchat: 没有写入本地历史：%v\n", err)
	} else if err := appendHistory(path, historyEntry{
		Server: server, ChatID: cred.ChatID, ParticipationID: cred.ParticipationID,
		DisplayName: cred.DisplayName, JoinedAt: resp.JoinedAt,
	}); err != nil {
		fmt.Fprintf(a.env.Stderr, "chatchat: 没有写入本地历史：%v\n", err)
	}
	if !a.json {
		fmt.Fprintln(a.env.Stderr, "已加入。上面一行是凭据行，只出现这一次：后续命令把它作为 stdin 第一行传入，不要发到 chat 里。")
	}
	return nil
}

func (a *app) cmdState(args []string) error {
	fs := a.flags("state", "< 凭据行")
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
	fs := a.flags("read", "[--after N] [--limit N] < 凭据行")
	after := fs.Int64("after", 0, "读取 sequence 大于 N 的消息")
	limit := fs.Int("limit", 0, "最多读取条数（默认由服务端决定）")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *after < 0 || *limit < 0 {
		return usageErr("--after 和 --limit 不能是负数")
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
	fs := a.flags("wait", "--after N [--timeout S] < 凭据行")
	after := fs.Int64("after", 0, "等待 sequence 大于 N 的消息（必填）")
	timeout := fs.Int("timeout", -1, "最多等待秒数（默认为服务端的单次等待上限，0 表示只检查一次）")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !visited(fs, "after") {
		return usageErr("缺少 --after：传入已经读到的最后一条 sequence")
	}
	if *after < 0 {
		return usageErr("--after 不能是负数")
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
	fs := a.flags("send", "[--text 正文] [--request-id ID] < 凭据行（第二行起是正文）")
	text := fs.String("text", "", "消息正文；不给时从 stdin 第二行起读取")
	requestID := fs.String("request-id", "", "client_request_id；重试失败的发送时传入上次打印的值")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if visited(fs, "request-id") && (*requestID == "" || utf8.RuneCountInString(*requestID) > 64) {
		return usageErr("--request-id 必须是 1–64 个字符")
	}
	c, err := a.readCredential()
	if err != nil {
		return err
	}
	rest, err := io.ReadAll(a.stdin)
	if err != nil {
		return fmt.Errorf("读取 stdin 失败：%w", err)
	}
	body := strings.TrimRight(string(rest), "\r\n")
	if visited(fs, "text") {
		if strings.TrimSpace(body) != "" {
			return usageErr("正文只能二选一：--text 或 stdin 第二行起")
		}
		body = *text
	}
	if strings.TrimSpace(body) == "" {
		return usageErr("消息正文是空的：用 --text 或在 stdin 第二行起写正文")
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
		note = "（重复请求，返回已有消息）"
	}
	fmt.Fprintf(a.env.Stdout, "已发送 #%d%s\n", m.Sequence, note)
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
	fs := a.flags("propose-end", "[--reason 理由] < 凭据行")
	reason := fs.String("reason", "", "建议结束的理由（可选，最多 500 字）")
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
	fmt.Fprintf(a.env.Stdout, "已建议结束 #%d，由创建者决定是否销毁\n", m.Sequence)
	return nil
}

func (a *app) cmdLeave(args []string) error {
	fs := a.flags("leave", "< 凭据行")
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
			fmt.Fprintf(a.env.Stderr, "chatchat: 没有更新本地历史：%v\n", err)
		}
	}
	if a.json {
		a.printRaw(raw)
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "已退出 %s，这组凭据已失效\n", c.ParticipationID)
	return nil
}

func (a *app) cmdHistory(args []string) error {
	fs := a.flags("history", "[--remote]")
	remote := fs.Bool("remote", false, "查询服务端上的参与记录（需要用户 token）")
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
		return fmt.Errorf("读取本地历史失败：%w", err)
	}
	if a.json {
		if entries == nil {
			entries = []historyEntry{}
		}
		writeJSONLine(a.env.Stdout, map[string]any{"participations": entries})
		return nil
	}
	if len(entries) == 0 {
		fmt.Fprintln(a.env.Stdout, "本地没有参与记录")
		return nil
	}
	for _, e := range entries {
		left := "未退出"
		if e.LeftAt != nil {
			left = "退出于 " + localTime(*e.LeftAt)
		}
		fmt.Fprintf(a.env.Stdout, "%s  %s  %s  %s  加入于 %s  %s\n",
			e.ChatID, e.ParticipationID, e.DisplayName, e.Server, localTime(e.JoinedAt), left)
	}
	fmt.Fprintln(a.env.Stdout, "历史只用于查找和排查，不能恢复身份；继续参与需要重新 join")
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
		fmt.Fprintln(a.env.Stdout, "服务端没有你的参与记录")
		return nil
	}
	for _, p := range resp.Participations {
		end := "未结束"
		if p.EndReason != nil {
			end = map[string]string{"left": "已退出", "chat_destroyed": "chat 已销毁"}[*p.EndReason]
			if p.LeftAt != nil {
				end += "（" + localTime(*p.LeftAt) + "）"
			}
		}
		fmt.Fprintf(a.env.Stdout, "%s  %s  %s  %s  加入于 %s  %s\n",
			p.ChatID, p.ParticipationID, kindName(p.Kind), p.DisplayName, localTime(p.JoinedAt), end)
	}
	return nil
}
