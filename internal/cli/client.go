package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// protocolVersion is the structured protocol this CLI speaks.
const protocolVersion = 1

// requestTimeout bounds every API call except the long poll of wait.
const requestTimeout = 30 * time.Second

// client calls one chatchat server with one credential.
type client struct {
	http      *http.Client
	base      string
	userAgent string
	// authorize sets the credential headers; nil sends none.
	authorize func(*http.Request)
	// waitMax is the server's cap on a single wait.
	waitMax time.Duration
}

// meta is the part of GET /api/v1/meta the CLI uses.
type meta struct {
	ProtocolVersion int    `json:"protocol_version"`
	ServerVersion   string `json:"server_version"`
	Limits          struct {
		WaitMaxSeconds int `json:"wait_max_seconds"`
	} `json:"limits"`
}

// connect returns a client for the server after checking that it speaks
// this CLI's protocol, so nothing runs against an incompatible server.
func (a *app) connect(server string, authorize func(*http.Request)) (*client, meta, error) {
	c := &client{http: a.env.HTTP, base: server, userAgent: "chatchat/" + a.env.Version}
	var m meta
	if _, _, err := c.call(a.ctx, "GET", "/api/v1/meta", nil, nil, &m, requestTimeout); err != nil {
		return nil, m, err
	}
	if m.ProtocolVersion != protocolVersion {
		return nil, m, protocolErr(strconv.Itoa(m.ProtocolVersion))
	}
	c.authorize = authorize
	c.waitMax = time.Duration(m.Limits.WaitMaxSeconds) * time.Second
	if c.waitMax <= 0 {
		c.waitMax = 60 * time.Second
	}
	return c, m, nil
}

func protocolErr(got string) error {
	return &cliError{
		exit: exitProtocol, code: "protocol_mismatch",
		msg:  fmt.Sprintf("服务端协议版本是 %s，本 CLI 支持 %d", got, protocolVersion),
		hint: "升级 chatchat CLI 后再试",
	}
}

func bearerAuth(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// apiError is an error response from the server.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// netError is a failure to get any response; the request may or may not
// have reached the server.
type netError struct{ err error }

func (e *netError) Error() string { return e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

// call sends a request and decodes a successful JSON response into out,
// returning the status and the raw body as well. Failures come back as
// *cliError, wrapping an *apiError or *netError.
func (c *client) call(ctx context.Context, method, path string, query url.Values, body, out any, timeout time.Duration) (int, []byte, error) {
	status, raw, err := c.roundTrip(ctx, method, path, query, body, timeout)
	if err != nil {
		return status, raw, classify(err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return status, raw, &cliError{exit: exitError, code: "bad_response", msg: "服务端响应无法解析"}
		}
	}
	return status, raw, nil
}

func (c *client) roundTrip(ctx context.Context, method, path string, query url.Values, body any, timeout time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.authorize != nil {
		c.authorize(req)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, &netError{stripURL(err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, nil, &netError{err}
	}
	proto := resp.Header.Get("Chatchat-Protocol")
	switch {
	case proto == "" && resp.StatusCode >= 500:
		// A proxy in front of a server that is down or restarting.
		return resp.StatusCode, raw, &apiError{resp.StatusCode, "unavailable", "服务端暂时不可用（HTTP " + strconv.Itoa(resp.StatusCode) + "）"}
	case proto == "":
		return resp.StatusCode, raw, &cliError{
			exit: exitError, code: "not_chatchat",
			msg:  fmt.Sprintf("%s 返回了 HTTP %d，看起来不是 chatchat 服务端", c.base, resp.StatusCode),
			hint: "检查 --server、CHATCHAT_SERVER 或配置文件里的服务端地址",
		}
	case proto != strconv.Itoa(protocolVersion):
		return resp.StatusCode, raw, protocolErr(proto)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Code == "" {
			e.Code, e.Error = "http_"+strconv.Itoa(resp.StatusCode), "HTTP "+resp.Status
		}
		return resp.StatusCode, raw, &apiError{resp.StatusCode, e.Code, e.Error}
	}
	return resp.StatusCode, raw, nil
}

// stripURL drops the request URL from a transport error; it says nothing
// the user does not know and keeps query strings out of the output.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// classify turns a call failure into a cliError with its exit code and
// what to do next.
func classify(err error) error {
	var ce *cliError
	if errors.As(err, &ce) {
		return err
	}
	var ne *netError
	if errors.As(err, &ne) {
		msg := "无法连接服务端：" + ne.Error()
		if errors.Is(ne.err, context.DeadlineExceeded) {
			msg = "请求服务端超时"
		}
		return &cliError{exit: exitError, code: "network", msg: msg}
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		return &cliError{exit: exitError, code: "error", msg: err.Error()}
	}
	out := &cliError{exit: exitError, code: ae.code, msg: ae.msg}
	switch ae.code {
	case "invalid_credentials":
		out.exit, out.hint = exitCredentials, "检查传入的凭据行是否完整；无法确认时重新 join"
	case "chat_mismatch":
		out.exit, out.hint = exitCredentials, "换用该 chat 对应的凭据行"
	case "wrong_credential_type":
		out.exit, out.hint = exitCredentials, "参与者命令用 join 输出的凭据行，账号命令用配置里的用户 token"
	case "unauthenticated", "token_revoked":
		out.exit, out.hint = exitCredentials, "在网站生成用户 token，用 chatchat config set-token 保存"
	case "request_id_conflict":
		out.exit, out.hint = exitConflict, "这个 request id 已用于另一段正文；新消息不要带 --request-id"
	case "participation_left":
		out.exit, out.hint = exitLeft, "需要继续参与就重新 join，成为新参与者"
	case "chat_destroyed":
		out.exit, out.hint = exitDestroyed, "停止参与，不必再 join"
	}
	return out
}

// retryable reports whether a failed send may be retried with the same
// request id: the request may not have reached the server, or the server
// failed in a way that may pass.
func retryable(err error) bool {
	var ne *netError
	if errors.As(err, &ne) {
		return true
	}
	var ae *apiError
	return errors.As(err, &ae) && ae.status >= 500
}

// waitTimeout is how long the HTTP call of one wait may take: the wait
// itself plus slack for the response.
func waitTimeout(wait time.Duration) time.Duration { return wait + requestTimeout }

// path joins escaped segments onto an API prefix.
func apiPath(prefix string, segments ...string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}
