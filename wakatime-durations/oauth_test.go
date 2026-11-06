package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testClientID     = "client-123"
	testClientSecret = "secret-456"
)

// fakeTokenServer imitates https://wakatime.com/oauth/token.
type fakeTokenServer struct {
	mu    sync.Mutex
	forms []url.Values
	fail  bool
}

func (f *fakeTokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f.mu.Lock()
	f.forms = append(f.forms, r.PostForm)
	fail := f.fail
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fail || r.PostForm.Get("client_secret") != testClientSecret || r.PostForm.Get("code") != "good-code" {
		w.WriteHeader(http.StatusBadRequest)
		// A real server might echo inputs; the client must not repeat them.
		io.WriteString(w, `{"error":"invalid_grant","echo":"`+r.PostForm.Get("client_secret")+`"}`)
		return
	}
	io.WriteString(w, `{"access_token":"waka_tok_abc","refresh_token":"waka_ref_def","token_type":"bearer","scope":"read_summaries,read_heartbeats"}`)
}

type browserSpy struct {
	mu   sync.Mutex
	urls []string
}

func (b *browserSpy) open(u string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.urls = append(b.urls, u)
	return nil
}

func (b *browserSpy) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.urls)
}

func newLoginManager(t *testing.T, tokenURL string, spy *browserSpy) (*LoginManager, string) {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "cfg", "token.json")
	return &LoginManager{Config: OAuthConfig{
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		RedirectURI:  "http://127.0.0.1:0/callback",
		Scope:        defaultScope,
		AuthorizeURL: "https://wakatime.example/oauth/authorize",
		TokenURL:     tokenURL,
		TokenPath:    tokenPath,
		HTTP:         &http.Client{Timeout: 5 * time.Second},
		OpenBrowser:  spy.open,
		Timeout:      time.Minute,
	}}, tokenPath
}

// authorizeParams returns the query of the authorization URL.
func authorizeParams(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

// redirectBack simulates the browser returning from WakaTime.
func redirectBack(t *testing.T, redirect string, q url.Values) *http.Response {
	t.Helper()
	resp, err := http.Get(redirect + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestOAuthLoginFlow(t *testing.T) {
	tokens := &fakeTokenServer{}
	ts := httptest.NewServer(tokens)
	defer ts.Close()
	spy := &browserSpy{}
	m, tokenPath := newLoginManager(t, ts.URL, spy)

	p, err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	if spy.count() != 1 || spy.urls[0] != p.url {
		t.Fatalf("browser not opened with the sign-in URL: %v", spy.urls)
	}
	q := authorizeParams(t, p.url)
	if q.Get("client_id") != testClientID || q.Get("response_type") != "code" ||
		q.Get("scope") != "read_summaries,read_heartbeats" || len(q.Get("state")) < 32 {
		t.Fatalf("bad authorize params: %v", q)
	}
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || strings.HasSuffix(redirect, ":0/callback") {
		t.Fatalf("redirect must use the real port: %s", redirect)
	}

	// A second Start while pending joins the same flow.
	p2, err := m.Start()
	if err != nil || p2 != p || spy.count() != 1 {
		t.Fatalf("second Start must reuse the pending login (err=%v, opens=%d)", err, spy.count())
	}

	// A forged redirect with a wrong state is rejected and changes nothing.
	if r := redirectBack(t, redirect, url.Values{"code": {"good-code"}, "state": {"forged"}}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong state: want 400, got %d", r.StatusCode)
	}
	select {
	case <-p.done:
		t.Fatal("login must still be pending after a forged redirect")
	default:
	}

	if r := redirectBack(t, redirect, url.Values{"code": {"good-code"}, "state": {q.Get("state")}}); r.StatusCode != http.StatusOK {
		t.Fatalf("callback: want 200, got %d", r.StatusCode)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	form := tokens.forms[len(tokens.forms)-1]
	if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != testClientID ||
		form.Get("redirect_uri") != redirect {
		t.Fatalf("bad token request: %v", form)
	}

	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", info.Mode().Perm())
	}
	tok, err := loadToken(tokenPath)
	if err != nil || tok.AccessToken != "waka_tok_abc" || tok.RefreshToken != "waka_ref_def" {
		t.Fatalf("bad stored token: %+v %v", tok, err)
	}

	auth, err := resolveAuth(func(string) string { return "" }, t.TempDir(), tokenPath)
	if err != nil || auth.Header != "Bearer waka_tok_abc" || !strings.Contains(auth.Method, "OAuth") {
		t.Fatalf("stored token not used: %+v %v", auth, err)
	}

	// After completion a new Start begins a fresh flow.
	p3, err := m.Start()
	if err != nil || p3 == p {
		t.Fatalf("expected a new login after completion: %v", err)
	}
}

func TestOAuthRefusedAndFailures(t *testing.T) {
	tokens := &fakeTokenServer{}
	ts := httptest.NewServer(tokens)
	defer ts.Close()

	// The user clicks Deny.
	m, tokenPath := newLoginManager(t, ts.URL, &browserSpy{})
	p, err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	q := authorizeParams(t, p.url)
	redirectBack(t, q.Get("redirect_uri"), url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})
	if err := p.Wait(); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("want access_denied, got %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatal("no token file expected after a refused sign-in")
	}

	// The token endpoint rejects the code: the error must not leak secrets.
	m2, tokenPath2 := newLoginManager(t, ts.URL, &browserSpy{})
	p2, err := m2.Start()
	if err != nil {
		t.Fatal(err)
	}
	q2 := authorizeParams(t, p2.url)
	redirectBack(t, q2.Get("redirect_uri"), url.Values{"code": {"bad-code"}, "state": {q2.Get("state")}})
	err = p2.Wait()
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("want invalid_grant, got %v", err)
	}
	if strings.Contains(err.Error(), testClientSecret) || strings.Contains(err.Error(), "bad-code") {
		t.Fatalf("error leaks a secret: %v", err)
	}
	if _, err := os.Stat(tokenPath2); !os.IsNotExist(err) {
		t.Fatal("no token file expected after a failed exchange")
	}

	// No OAuth app configured.
	if _, err := (&LoginManager{Config: OAuthConfig{RedirectURI: defaultRedirectURI}}).Start(); !errors.Is(err, errOAuthNotConfigured) {
		t.Fatalf("want errOAuthNotConfigured, got %v", err)
	}

	// Non-loopback redirect URIs are refused.
	m3, _ := newLoginManager(t, ts.URL, &browserSpy{})
	m3.Config.RedirectURI = "http://example.com/callback"
	if _, err := m3.Start(); err == nil {
		t.Fatal("non-loopback redirect must be refused")
	}
}

func TestOAuthSecondProcessDoesNotOpenBrowser(t *testing.T) {
	ts := httptest.NewServer(&fakeTokenServer{})
	defer ts.Close()
	spy1, spy2 := &browserSpy{}, &browserSpy{}
	m1, _ := newLoginManager(t, ts.URL, spy1)
	p1, err := m1.Start()
	if err != nil {
		t.Fatal(err)
	}
	// Cursor may start the server twice; the second process gets the same
	// fixed redirect port and must fail before opening a browser.
	m2, _ := newLoginManager(t, ts.URL, spy2)
	m2.Config.RedirectURI = authorizeParams(t, p1.url).Get("redirect_uri")
	if _, err := m2.Start(); err == nil {
		t.Fatal("second listener on the same port must fail")
	}
	if spy2.count() != 0 {
		t.Fatal("second process must not open a browser")
	}
}

func TestAuthOrderWithStoredToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	os.WriteFile(filepath.Join(dir, ".wakatime.cfg"), []byte("[settings]\napi_key = cfg-key\n"), 0o600)
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if a, _ := resolveAuth(getenv, dir, tokenPath); a.Header != basic("cfg-key") {
		t.Fatalf("without a stored token the cfg key is used: %q", a.Header)
	}
	saveToken(tokenPath, StoredToken{AccessToken: "stored", CreatedAt: time.Now()})
	if a, _ := resolveAuth(getenv, dir, tokenPath); a.Header != "Bearer stored" {
		t.Fatalf("stored token must beat the cfg key: %q", a.Header)
	}
	env["WAKATIME_API_KEY"] = "env-key"
	if a, _ := resolveAuth(getenv, dir, tokenPath); a.Header != basic("env-key") {
		t.Fatalf("env key must beat the stored token: %q", a.Header)
	}
	delete(env, "WAKATIME_API_KEY")
	saveToken(tokenPath, StoredToken{AccessToken: "old", ExpiresAt: time.Now().Add(-time.Hour)})
	if a, _ := resolveAuth(getenv, dir, tokenPath); a.Header != basic("cfg-key") {
		t.Fatalf("an expired token must be skipped: %q", a.Header)
	}
}

func TestMCPAutoLoginAndPrompt(t *testing.T) {
	var mu sync.Mutex
	logins := 0
	notified := make(chan string, 4)
	srv := &Server{
		Collect: func(Options) ([]Row, error) { return nil, errNoCredentials },
		Login: func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			logins++
			return "https://wakatime.example/oauth/authorize?state=x", nil
		},
		HasCredentials: func() bool { return false },
		Status:         func() string { return "Not signed in." },
		Logout:         func() error { return nil },
		Notify:         func(m string) { notified <- m },
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wakatime_branch_durations","arguments":{"date":"2026-11-06"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"wakatime_auth_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"wakatime_login","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"wakatime_logout","arguments":{}}}`,
		``,
	}, "\n")
	var out strings.Builder
	if err := serveMCP(strings.NewReader(input), &out, srv); err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-notified:
		if !strings.Contains(m, "https://wakatime.example/oauth/authorize") {
			t.Fatalf("notification must carry the sign-in URL: %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto sign-in did not start on initialize")
	}

	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad line %q", line)
		}
		resps = append(resps, m)
	}
	text := func(i int) (string, bool) {
		r := resps[i]["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"].(bool)
	}
	if s, isErr := text(2); !isErr || !strings.Contains(s, "Sign in here: https://wakatime.example/oauth/authorize") {
		t.Fatalf("durations without credentials must return the sign-in link: %q", s)
	}
	if s, _ := text(3); s != "Not signed in." {
		t.Fatalf("bad status: %q", s)
	}
	if s, isErr := text(4); isErr || !strings.Contains(s, "https://wakatime.example/oauth/authorize") {
		t.Fatalf("bad login result: %q", s)
	}
	if s, isErr := text(5); isErr || !strings.Contains(s, "Signed out") {
		t.Fatalf("bad logout result: %q", s)
	}

	mu.Lock()
	defer mu.Unlock()
	// One automatic start (despite two initialize calls), one from the
	// durations prompt, one from wakatime_login.
	if logins != 3 {
		t.Fatalf("want 3 Login calls, got %d", logins)
	}
}
