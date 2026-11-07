// Package mcp is HAL9001's native Model Context Protocol client.
//
// It speaks MCP through the official Go SDK: stdio for local servers and
// Streamable HTTP for remote ones. The client is deliberately narrow.
//
// Everything a server sends is untrusted input. A tool description, a
// result, a resource document and a prompt are data, never instructions,
// and they never carry authority: the permission decision comes from
// HAL's own gate and from the operator's local configuration, never from
// a server annotation.
//
// The model sees MCP tools only through HAL's ordinary tool surface, so
// every call keeps the same evidence, activity and cancellation
// behaviour as HAL's built-in tools.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport is the wire transport of one MCP server.
type Transport string

const (
	// TransportStdio runs a local server as a child process and speaks
	// newline-delimited JSON over its standard streams.
	TransportStdio Transport = "stdio"
	// TransportHTTP speaks Streamable HTTP to a remote server.
	TransportHTTP Transport = "http"
)

// Bounds. Every value a server can influence is cut before it reaches
// the model, the session or the audit trail.
const (
	// DefaultTimeout bounds one MCP call.
	DefaultTimeout = 30 * time.Second
	// DefaultConnectTimeout bounds the handshake with one server.
	DefaultConnectTimeout = 20 * time.Second
	// MaxServers bounds how many servers one HAL may hold.
	MaxServers = 16
	// MaxToolsPerServer bounds how many tools one server may register.
	MaxToolsPerServer = 128
	// MaxResourcesPerServer bounds how many resources one server may list.
	MaxResourcesPerServer = 256
	// MaxPromptsPerServer bounds how many prompts one server may list.
	MaxPromptsPerServer = 256
	// maxListPages bounds a paginated listing. A cursor that repeats ends
	// the listing even earlier.
	maxListPages = 64
	// MaxSchemaBytes bounds one tool schema.
	MaxSchemaBytes = 64 << 10
	// MaxDescriptionBytes bounds one tool description.
	MaxDescriptionBytes = 1024
	// MaxResultBytes bounds what one tool result may add to a turn.
	MaxResultBytes = 48 << 10
	// MaxArgumentBytes bounds the arguments of one call.
	MaxArgumentBytes = 256 << 10
	// MaxResourceBytes bounds one resource document.
	MaxResourceBytes = 64 << 10
	// MaxPromptBytes bounds one prompt document.
	MaxPromptBytes = 48 << 10
	// MaxNameLen bounds a configured server name.
	MaxNameLen = 64
	// MaxRemoteToolNameBytes bounds a server-supplied tool name. A tool
	// with a longer name is not offered: the name is used in the activity
	// line, the ledger and the audit trail.
	MaxRemoteToolNameBytes = 128
	// MaxRemoteNameBytes bounds other server-supplied display names.
	MaxRemoteNameBytes = 256
	// MaxResourceURIBytes bounds a server-supplied resource URI.
	MaxResourceURIBytes = 2048
)

// ErrUnknownServer reports a server name that is not configured.
var ErrUnknownServer = errors.New("mcp: unknown server")

// ErrNoServers reports an empty MCP configuration.
var ErrNoServers = errors.New("mcp: no servers are configured")

// Server is one configured MCP server.
//
// The configuration is operator-controlled: HAL never creates a server,
// never edits one and never lets a model change one. A server that is
// not configured here does not exist.
type Server struct {
	// Name identifies the server in the interface and in every exposed
	// tool name. It must be unique.
	Name string
	// Transport is "stdio" (the default) or "http".
	Transport Transport
	// Command and Args start a local server.
	Command string
	Args    []string
	// Env holds extra environment variables for a local server. A value
	// of the form ${NAME} is read from the environment at connect time.
	Env map[string]string
	// CWD is the working directory of a local server. Empty means the
	// directory HAL was started in.
	CWD string
	// URL is the endpoint of a remote server.
	URL string
	// Headers are extra HTTP headers. A value of the form ${NAME} is
	// read from the environment at connect time.
	Headers map[string]string
	// TokenEnv names an environment variable holding a bearer token.
	// The token is sent as "Authorization: Bearer <token>" and is never
	// written to a log, a prompt or a session.
	TokenEnv string
	// Timeout bounds one call to this server.
	Timeout time.Duration
	// AllowedTools names the tools this server may run in 444. It is the
	// trusted local policy: a tool that is not named here needs 666,
	// whatever the server says about itself.
	AllowedTools []string
}

// namePattern is the shape a server name may take. The name becomes
// part of every exposed tool name, so it stays in the provider's
// function-name alphabet.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Validate reports whether the configuration can be used at all.
//
// The checks are about shape, not about trust: a valid configuration can
// still point at a hostile server.
func (s *Server) Validate() error {
	if s == nil {
		return errors.New("mcp: no server configuration")
	}
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("mcp: server name %q must be 1-64 characters of A-Z, a-z, 0-9, _ or -", s.Name)
	}
	switch s.Transport {
	case "", TransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("mcp: server %q needs a command for the stdio transport", s.Name)
		}
	case TransportHTTP:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("mcp: server %q needs a url for the http transport", s.Name)
		}
		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("mcp: server %q has an invalid url: %w", s.Name, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("mcp: server %q must use http or https", s.Name)
		}
		if u.Host == "" {
			return fmt.Errorf("mcp: server %q has no host in its url", s.Name)
		}
	default:
		return fmt.Errorf("mcp: server %q has an unknown transport %q", s.Name, s.Transport)
	}
	if s.Timeout < 0 {
		return fmt.Errorf("mcp: server %q has a negative timeout", s.Name)
	}
	if s.Timeout > 10*time.Minute {
		return fmt.Errorf("mcp: server %q has a timeout longer than ten minutes", s.Name)
	}
	if s.CWD != "" {
		info, err := os.Stat(s.CWD)
		if err != nil {
			return fmt.Errorf("mcp: server %q has an unusable working directory: %w", s.Name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("mcp: server %q working directory is not a directory", s.Name)
		}
	}
	for key, value := range s.Env {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("mcp: server %q has an invalid environment variable name", s.Name)
		}
		if strings.ContainsAny(value, "\x00") {
			return fmt.Errorf("mcp: server %q has an invalid value for %s", s.Name, key)
		}
	}
	for key, value := range s.Headers {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, ":\r\n\x00") {
			return fmt.Errorf("mcp: server %q has an invalid header name", s.Name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("mcp: server %q has a header value with a line break", s.Name)
		}
	}
	for _, tool := range s.AllowedTools {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("mcp: server %q has an empty allowed tool", s.Name)
		}
	}
	return nil
}

// validRemoteName reports whether a server-supplied identifier is usable
// and safe to store in an activity line, ledger or audit record.
func validRemoteName(name string, max int) bool {
	if name == "" || len(name) > max {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

// cleanRemoteText bounds and strips a server-supplied display string.
func cleanRemoteText(text string, max int) string {
	out := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, text)
	if len(out) <= max {
		return out
	}
	var b strings.Builder
	for _, r := range out {
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// timeout is the effective call timeout for this server.
func (s Server) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// allowsIn444 reports whether the operator's local policy permits this
// tool in 444. The policy is the configuration, never a server
// annotation.
func (s Server) allowsIn444(tool string) bool {
	for _, allowed := range s.AllowedTools {
		if allowed == tool {
			return true
		}
	}
	return false
}

// transport builds the SDK transport for this server.
//
// The returned secrets are the resolved credentials, so an error message
// can be redacted before it reaches a log, the interface or a model. The
// close function tears the transport down even when the handshake failed,
// so a local server never outlives the attempt to reach it.
func (s Server) transport() (sdk.Transport, []string, func(), error) {
	switch s.Transport {
	case "", TransportStdio:
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = baseEnvironment()
		for key, value := range s.Env {
			resolved, ok := expandValue(value)
			if !ok {
				return nil, nil, nil, fmt.Errorf("mcp: server %q needs the environment variable %s", s.Name, referenceName(value))
			}
			cmd.Env = append(cmd.Env, key+"="+resolved)
		}
		if s.CWD != "" {
			cmd.Dir = s.CWD
		}
		managed := &managedTransport{inner: &sdk.CommandTransport{
			Command:           cmd,
			TerminateDuration: 5 * time.Second,
		}}
		return managed, nil, managed.close, nil
	case TransportHTTP:
		headers, secrets, err := s.httpHeaders()
		if err != nil {
			return nil, nil, nil, err
		}
		client := &http.Client{
			Transport: &headerTransport{
				headers: headers,
				origin:  originOf(s.URL),
			},
			// A redirect is not followed: an MCP endpoint is configured
			// explicitly, and forwarding a credential to a different host
			// is never acceptable.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		managed := &managedTransport{
			inner: &sdk.StreamableClientTransport{
				Endpoint:   s.URL,
				HTTPClient: client,
				MaxRetries: 2,
			},
		}
		return managed, secrets, managed.close, nil
	default:
		return nil, nil, nil, fmt.Errorf("mcp: server %q has an unknown transport %q", s.Name, s.Transport)
	}
}

// httpHeaders resolves the configured headers and the bearer token.
//
// The resolved values are secrets: they are never logged and never
// stored, and an error that quotes one is redacted.
func (s Server) httpHeaders() (map[string]string, []string, error) {
	headers := make(map[string]string, len(s.Headers)+1)
	var secrets []string
	for key, value := range s.Headers {
		resolved, ok := expandValue(value)
		if !ok {
			return nil, nil, fmt.Errorf("mcp: server %q needs the environment variable %s", s.Name, referenceName(value))
		}
		if isSecretHeader(key) {
			secrets = append(secrets, resolved)
		}
		headers[key] = resolved
	}
	if s.TokenEnv != "" {
		token, ok := os.LookupEnv(s.TokenEnv)
		if !ok || strings.TrimSpace(token) == "" {
			return nil, nil, fmt.Errorf("mcp: server %q needs the environment variable %s", s.Name, s.TokenEnv)
		}
		headers["Authorization"] = "Bearer " + token
		secrets = append(secrets, token)
	}
	return headers, secrets, nil
}

// isSecretHeader reports whether a header carries a credential.
func isSecretHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "cookie", "x-api-key", "api-key":
		return true
	}
	return false
}

// managedTransport keeps the SDK's connection so a failed handshake can
// still be torn down. Without it, a local server whose handshake fails
// would keep running with no owner.
type managedTransport struct {
	inner sdk.Transport

	conn sdk.Connection
}

// Connect implements sdk.Transport.
func (t *managedTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.conn = conn
	return conn, nil
}

// close tears the connection down. It is safe to call more than once.
func (t *managedTransport) close() {
	if t.conn == nil {
		return
	}
	_ = t.conn.Close()
	t.conn = nil
}

// headerTransport adds the configured headers to every request that goes
// to the configured origin.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
	origin  string
}

// RoundTrip implements http.RoundTripper.
func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.origin == "" || originOf(req.URL.String()) == t.origin {
		for key, value := range t.headers {
			clone.Header.Set(key, value)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// originOf is the scheme and host of a URL, in the form used to decide
// whether a request may receive the configured headers.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// inheritedEnvironment is the small set of variables a local server
// receives without being named in its configuration. Everything else —
// provider keys, search keys, MPM tokens — stays out of the child. A
// server that needs another variable asks for it explicitly with an
// "env" entry, whose value may reference the parent environment as
// ${NAME}.
var inheritedEnvironment = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "LANG": true, "LANGUAGE": true,
	"LC_ALL": true, "LC_CTYPE": true, "LC_MESSAGES": true,
	"TZ": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"NODE_EXTRA_CA_CERTS": true,
	"HTTP_PROXY":          true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"SystemRoot": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
}

// baseEnvironment is the environment a local server inherits. Secrets
// are not included by default: the operator passes the ones a server
// actually needs through its own "env" entries.
func baseEnvironment() []string {
	environ := os.Environ()
	out := make([]string, 0, len(inheritedEnvironment))
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if inheritedEnvironment[key] {
			out = append(out, entry)
		}
	}
	return out
}

// expandValue resolves ${NAME} references in a configured value.
//
// A value without a reference is used as it is. A reference to a
// variable that is not set is an error: a server must never start with
// an empty credential where one was configured.
func expandValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "${") {
		return value, true
	}
	var out strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			out.WriteString(value)
			break
		}
		out.WriteString(value[:start])
		rest := value[start+2:]
		end := strings.Index(rest, "}")
		if end < 0 {
			// An unterminated reference is a configuration mistake.
			return "", false
		}
		name := rest[:end]
		resolved, ok := os.LookupEnv(name)
		if !ok {
			return "", false
		}
		out.WriteString(resolved)
		value = rest[end+1:]
	}
	return out.String(), true
}

// referenceName names the environment variable a value refers to, for
// an error message that must not quote the value itself.
func referenceName(value string) string {
	start := strings.Index(value, "${")
	if start < 0 {
		return "(a value)"
	}
	rest := value[start+2:]
	end := strings.Index(rest, "}")
	if end < 0 {
		return "(a value)"
	}
	return rest[:end]
}

// redact removes every secret from a message.
func redact(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text
}

// safeURL renders a server URL without credentials, query parameters or a
// fragment. A query string can carry a token even when the URL has no
// user info, so it is not shown either.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid url)"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
