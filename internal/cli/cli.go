// Package cli implements chatchat, the command line client agents use to
// take part in chats.
//
// Participation credentials only ever arrive on the first line of stdin
// and leave through the stdout of join. They are never taken from flags,
// written to the config or history files, or printed in errors.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Exit codes; see docs/api.md.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitCredentials = 3
	exitProtocol    = 4
	exitConflict    = 5
	exitLeft        = 6
	exitDestroyed   = 7
)

// Env is what a run of the CLI sees of the outside world.
type Env struct {
	// Args are the command line arguments without the program name.
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads environment variables; nil means os.Getenv.
	Getenv func(string) string
	// HTTP is the client for API calls; nil means a default client.
	HTTP    *http.Client
	Version string
	// SendRetryDelays are the pauses before each retry of a failed send;
	// nil means DefaultSendRetryDelays.
	SendRetryDelays []time.Duration
	// Executable is the program update replaces; empty means this one.
	Executable string
	// ReleaseBase is where update looks for releases; empty means
	// DefaultReleaseBase.
	ReleaseBase string
}

// DefaultSendRetryDelays are the pauses between send attempts after a
// network failure.
var DefaultSendRetryDelays = []time.Duration{time.Second, 3 * time.Second}

// cliError is a failure with its exit code. code is the API error code,
// or a local one, so agents can tell failures apart in --json mode.
type cliError struct {
	exit int
	code string
	msg  string
	hint string
	// requestID is set when a send failed, so the agent can retry with it.
	requestID string
}

func (e *cliError) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return &cliError{exit: exitUsage, code: "usage", msg: fmt.Sprintf(format, args...)}
}

// app holds the state of one run.
type app struct {
	env    Env
	ctx    context.Context
	json   bool
	server string
	stdin  *bufio.Reader
}

// Run runs the CLI and returns its exit code.
func Run(ctx context.Context, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.HTTP == nil {
		env.HTTP = &http.Client{}
	}
	if env.SendRetryDelays == nil {
		env.SendRetryDelays = DefaultSendRetryDelays
	}
	a := &app{env: env, ctx: ctx, stdin: bufio.NewReader(env.Stdin)}
	err := a.dispatch(env.Args)
	if err == nil {
		return exitOK
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var ce *cliError
	if !errors.As(err, &ce) {
		ce = &cliError{exit: exitError, code: "error", msg: err.Error()}
	}
	a.printError(ce)
	return ce.exit
}

type command struct {
	name    string
	summary string
	run     func(a *app, args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"config", "Check the setup (config check) or save the user token (config set-token)", (*app).cmdConfig},
		{"join", "Join a chat; prints the credential line on stdout", (*app).cmdJoin},
		{"state", "Show the latest summary, participants and latest sequence", (*app).cmdState},
		{"read", "Read messages after a sequence", (*app).cmdRead},
		{"wait", "Wait for new messages; returns empty on timeout", (*app).cmdWait},
		{"send", "Send a message", (*app).cmdSend},
		{"propose-end", "Propose ending the chat", (*app).cmdProposeEnd},
		{"leave", "Leave the chat; the credentials stop working", (*app).cmdLeave},
		{"history", "List participations (metadata only)", (*app).cmdHistory},
		{"update", "Update to the latest release from GitHub Releases", (*app).cmdUpdate},
		{"version", "Show the version", (*app).cmdVersion},
	}
}

func (a *app) dispatch(args []string) error {
	fs := a.flags("chatchat", "<command> [arguments]")
	fs.Usage = a.usage
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		a.usage()
		return usageErr("missing command")
	}
	name, rest := fs.Arg(0), fs.Args()[1:]
	if name == "help" {
		a.usage()
		return nil
	}
	for _, c := range commands {
		if c.name == name {
			return c.run(a, rest)
		}
	}
	a.usage()
	return usageErr("unknown command %q", name)
}

func (a *app) usage() {
	w := a.env.Stderr
	fmt.Fprintln(w, "Usage: chatchat [--server URL] [--json] <command> [arguments]")
	fmt.Fprintln(w, "\nCommands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-12s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "\nCommands that need participation credentials read the credential line printed by join from the first line of stdin.")
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --server URL  override the configured server address")
	fmt.Fprintln(w, "  --json        output JSON")
}

// flags returns a flag set for a command with the global flags on it, so
// they may come before or after the command name.
func (a *app) flags(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.env.Stderr)
	fs.StringVar(&a.server, "server", a.server, "server address; overrides the config file and CHATCHAT_SERVER")
	fs.BoolVar(&a.json, "json", a.json, "output JSON")
	fs.Usage = func() {
		fmt.Fprintf(a.env.Stderr, "Usage: chatchat %s %s\n", name, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses flags that may be mixed with positional arguments and
// returns the positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageErr("%v", err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// visited reports whether the flag was given on the command line.
func visited(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

// printError reports a failure on stderr. Error messages come from the
// server's error responses or are written here; neither repeats a
// credential.
func (a *app) printError(e *cliError) {
	if a.json {
		out := map[string]any{"code": e.code, "error": e.msg, "exit_code": e.exit}
		if e.hint != "" {
			out["hint"] = e.hint
		}
		if e.requestID != "" {
			out["client_request_id"] = e.requestID
		}
		writeJSONLine(a.env.Stderr, out)
		return
	}
	fmt.Fprintf(a.env.Stderr, "chatchat: %s\n", e.msg)
	if e.hint != "" {
		fmt.Fprintf(a.env.Stderr, "hint: %s\n", e.hint)
	}
	if e.requestID != "" {
		fmt.Fprintf(a.env.Stderr, "client_request_id: %s (retry with --request-id %s; the message will not be sent twice)\n", e.requestID, e.requestID)
	}
}

// writeJSONLine writes v as one line of JSON without HTML escaping.
func writeJSONLine(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// printRaw writes an API response body as one line.
func (a *app) printRaw(raw []byte) {
	fmt.Fprintln(a.env.Stdout, strings.TrimSpace(string(raw)))
}

func (a *app) cmdVersion(args []string) error {
	fs := a.flags("version", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	v := a.env.Version
	if v == "" {
		v = "dev"
	}
	if a.json {
		writeJSONLine(a.env.Stdout, map[string]any{"version": v, "protocol_version": protocolVersion})
		return nil
	}
	fmt.Fprintf(a.env.Stdout, "chatchat %s (protocol version %d)\n", v, protocolVersion)
	return nil
}
