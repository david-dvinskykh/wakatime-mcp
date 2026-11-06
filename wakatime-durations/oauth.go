package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthorizeURL = "https://wakatime.com/oauth/authorize"
	defaultTokenURL     = "https://wakatime.com/oauth/token"
	defaultRedirectURI  = "http://127.0.0.1:8977/callback"
	defaultScope        = "read_summaries,read_heartbeats"
	loginTimeout        = 10 * time.Minute
)

// errOAuthNotConfigured is returned when interactive sign-in is requested
// without an OAuth app.
var errOAuthNotConfigured = errors.New("interactive sign-in needs a WakaTime OAuth app: create one at https://wakatime.com/apps " +
	"with redirect URI " + defaultRedirectURI + " and set WAKATIME_CLIENT_ID and WAKATIME_CLIENT_SECRET")

// OAuthConfig describes the WakaTime OAuth 2.0 app and where the token lives.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scope        string
	AuthorizeURL string
	TokenURL     string
	TokenPath    string
	HTTP         *http.Client
	// OpenBrowser opens the authorization URL; replaceable in tests.
	OpenBrowser func(string) error
	Timeout     time.Duration
}

// defaultTokenPath is the stored-token file, e.g.
// ~/Library/Application Support/wakatime-durations/token.json on macOS.
func defaultTokenPath(getenv func(string) string) string {
	if p := strings.TrimSpace(getenv("WAKATIME_TOKEN_FILE")); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "wakatime-durations", "token.json")
}

func oauthConfigFromEnv(getenv func(string) string, tokenPath string) OAuthConfig {
	redirect := strings.TrimSpace(getenv("WAKATIME_OAUTH_REDIRECT_URI"))
	if redirect == "" {
		redirect = defaultRedirectURI
	}
	open := openBrowser
	if getenv("WAKATIME_NO_BROWSER") != "" {
		open = func(string) error { return errors.New("browser disabled") }
	}
	return OAuthConfig{
		ClientID:     strings.TrimSpace(getenv("WAKATIME_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(getenv("WAKATIME_CLIENT_SECRET")),
		RedirectURI:  redirect,
		Scope:        defaultScope,
		AuthorizeURL: defaultAuthorizeURL,
		TokenURL:     defaultTokenURL,
		TokenPath:    tokenPath,
		HTTP:         &http.Client{Timeout: requestTimeout},
		OpenBrowser:  open,
		Timeout:      loginTimeout,
	}
}

// StoredToken is what the sign-in flow saves on disk.
type StoredToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (t StoredToken) valid(now time.Time) bool {
	return t.AccessToken != "" && (t.ExpiresAt.IsZero() || now.Before(t.ExpiresAt))
}

func loadToken(path string) (StoredToken, error) {
	var t StoredToken
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(b, &t)
	return t, err
}

func saveToken(path string, t StoredToken) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func deleteToken(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// LoginManager runs at most one browser sign-in at a time.
type LoginManager struct {
	Config  OAuthConfig
	mu      sync.Mutex
	pending *pendingLogin
}

type pendingLogin struct {
	url  string
	done chan struct{}
	err  error
}

// Start begins a sign-in, or returns the one already in progress, and
// returns the authorization URL for the user to open. The flow finishes in
// the background when the browser is redirected back.
func (m *LoginManager) Start() (*pendingLogin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		select {
		case <-m.pending.done:
		default:
			return m.pending, nil
		}
	}
	c := m.Config
	if c.ClientID == "" || c.ClientSecret == "" {
		return nil, errOAuthNotConfigured
	}
	ru, err := url.Parse(c.RedirectURI)
	if err != nil || ru.Scheme != "http" || !isLoopback(ru.Hostname()) {
		return nil, fmt.Errorf("redirect URI %q must be http on 127.0.0.1 or localhost", c.RedirectURI)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ru.Hostname(), portOrDefault(ru)))
	if err != nil {
		return nil, fmt.Errorf("cannot listen for the sign-in redirect on %s: %w", ru.Host, err)
	}
	// With port 0 (tests) the real port is only known after listening.
	if ru.Port() == "0" {
		ru.Host = net.JoinHostPort(ru.Hostname(), strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	}
	redirect := ru.String()
	path := ru.Path
	if path == "" {
		path = "/"
	}

	state, err := randomHex(24)
	if err != nil {
		ln.Close()
		return nil, err
	}
	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("scope", c.Scope)
	q.Set("state", state)
	p := &pendingLogin{url: c.AuthorizeURL + "?" + q.Encode(), done: make(chan struct{})}

	var once sync.Once
	finish := func(err error) {
		once.Do(func() {
			p.err = err
			close(p.done)
		})
	}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()
		if qs.Get("state") != state {
			http.Error(w, "State mismatch. Start the sign-in again.", http.StatusBadRequest)
			return
		}
		if e := qs.Get("error"); e != "" {
			writePage(w, "WakaTime sign-in was not completed: "+e)
			finish(fmt.Errorf("sign-in refused: %s", e))
			return
		}
		code := qs.Get("code")
		if code == "" {
			http.Error(w, "Missing code.", http.StatusBadRequest)
			return
		}
		tok, err := exchangeCode(c, redirect, code)
		if err == nil {
			err = saveToken(c.TokenPath, tok)
		}
		if err != nil {
			writePage(w, "WakaTime sign-in failed: "+err.Error())
			finish(err)
			return
		}
		writePage(w, "Signed in to WakaTime. You can close this tab and go back to Cursor.")
		finish(nil)
	})
	srv.Handler = mux
	go srv.Serve(ln)

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = loginTimeout
	}
	go func() {
		select {
		case <-p.done:
		case <-time.After(timeout):
			finish(errors.New("sign-in timed out; start it again"))
		}
		// Let the final page reach the browser before closing.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	m.pending = p
	if c.OpenBrowser != nil {
		_ = c.OpenBrowser(p.url)
	}
	return p, nil
}

// Wait blocks until the sign-in finishes.
func (p *pendingLogin) Wait() error {
	<-p.done
	return p.err
}

func exchangeCode(c OAuthConfig, redirect, code string) (StoredToken, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("redirect_uri", redirect)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	req, err := http.NewRequest(http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return StoredToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "wakatime-durations/1.0")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return StoredToken{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	fields := map[string]string{}
	var raw map[string]any
	if json.Unmarshal(body, &raw) == nil {
		for k, v := range raw {
			switch x := v.(type) {
			case string:
				fields[k] = x
			case float64:
				fields[k] = strconv.FormatFloat(x, 'f', -1, 64)
			}
		}
	} else if vals, err := url.ParseQuery(string(body)); err == nil {
		for k := range vals {
			fields[k] = vals.Get(k)
		}
	}
	if resp.StatusCode != http.StatusOK || fields["access_token"] == "" {
		// Report only the OAuth error code, never the response body.
		reason := fields["error"]
		if reason == "" {
			reason = "no access_token in response"
		}
		return StoredToken{}, fmt.Errorf("token request: HTTP %d: %s", resp.StatusCode, reason)
	}
	now := time.Now().UTC()
	t := StoredToken{
		AccessToken:  fields["access_token"],
		RefreshToken: fields["refresh_token"],
		TokenType:    fields["token_type"],
		Scope:        fields["scope"],
		CreatedAt:    now,
	}
	if s, err := strconv.ParseFloat(fields["expires_in"], 64); err == nil && s > 0 {
		t.ExpiresAt = now.Add(time.Duration(s) * time.Second)
	} else if e, err := time.Parse(time.RFC3339, fields["expires_at"]); err == nil {
		t.ExpiresAt = e
	}
	return t, nil
}

func writePage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>wakatime-durations</title>"+
		"<body style=\"font-family:system-ui;margin:3em\"><p>%s</p></body>", html.EscapeString(msg))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func portOrDefault(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return "80"
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	// Keep the helper's output away from stdout, which carries MCP.
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Start()
}
