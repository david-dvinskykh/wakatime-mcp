package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testDate = "2026-11-05"

var warsaw = mustLoc("Europe/Warsaw")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

// at returns Unix seconds for hh:mm:ss on testDate in Warsaw.
func at(h, m, s int) float64 {
	return float64(time.Date(2026, 11, 5, h, m, s, 0, warsaw).Unix())
}

type dur struct{ Time, Duration float64 }

// fakeWakaTime serves summaries and durations from fixed data and records
// every request it receives.
type fakeWakaTime struct {
	mu        sync.Mutex
	branches  map[string]map[string]float64 // project -> branch -> seconds
	durations map[string][]dur              // "project|branch" -> items
	requests  []*http.Request
	auths     []string
}

func (f *fakeWakaTime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.mu.Unlock()
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/summaries":
		if q.Get("start") != testDate || q.Get("end") != testDate || q.Get("timezone") == "" {
			http.Error(w, "bad query", 400)
			return
		}
		if p := q.Get("project"); p != "" {
			var bs []map[string]any
			for name, sec := range f.branches[p] {
				bs = append(bs, map[string]any{"name": name, "total_seconds": sec})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"branches": bs}}})
			return
		}
		var ps []map[string]any
		for name := range f.branches {
			ps = append(ps, map[string]any{"name": name})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"projects": ps}}})
	case "/durations":
		if q.Get("date") != testDate || q.Get("timezone") == "" {
			http.Error(w, "bad query", 400)
			return
		}
		items := []map[string]any{}
		for _, d := range f.durations[q.Get("project")+"|"+q.Get("branches")] {
			items = append(items, map[string]any{"time": d.Time, "duration": d.Duration})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": items})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeWakaTime) durationCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.URL.Path == "/durations" {
			out = append(out, r.URL.Query().Get("project")+"|"+r.URL.Query().Get("branches"))
		}
	}
	return out
}

func newFake() *fakeWakaTime {
	return &fakeWakaTime{
		branches: map[string]map[string]float64{
			"api": {"feature/CAP-123-login": 1500, "master": 600, "Unknown": 420, "tiny": 20},
			"web": {"": 300},
		},
		durations: map[string][]dur{
			"api|feature/CAP-123-login": {
				{at(9, 0, 0), 300},  // 09:00:00-09:05:00
				{at(9, 5, 30), 270}, // gap 30 s -> merged, ends 09:10:00
				{at(9, 11, 0), 60},  // gap 60 s -> merged, ends 09:12:00
				{at(11, 0, 0), 870}, // gap > 60 s -> new block
			},
			"api|master": {{at(10, 0, 0), 600}},
			"api|tiny":   {{at(12, 0, 0), 20}},
		},
	}
}

func testClient(url string) *Client {
	c := NewClient(Auth{Header: "Bearer test", Method: "test"})
	c.BaseURL = url
	c.Sleep = func(time.Duration) {}
	return c
}

func TestCSVOutput(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()

	rows, err := testClient(srv.URL).Collect(Options{Date: testDate, Timezone: "Europe/Warsaw", MinMinutes: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := csvString(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"project,branch,start,seconds",
		"api,feature/CAP-123-login,2026-11-05T09:00:00+01:00,720",
		"api,master,2026-11-05T10:00:00+01:00,600",
		"api,feature/CAP-123-login,2026-11-05T11:00:00+01:00,870",
		"api,Unknown,,420",
		"web,,,300",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("CSV mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMergeBlocks(t *testing.T) {
	in := []block{{100, 200}, {260, 300}, {361, 400}, {150, 180}, {400, 500}}
	got := mergeBlocks(in, 60)
	want := []block{{100, 300}, {361, 500}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestUnknownAndEmptyBranchSkipDurations(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	if _, err := testClient(srv.URL).Collect(Options{Date: testDate, Timezone: "Europe/Warsaw", MinMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.durationCalls() {
		if strings.HasSuffix(c, "|Unknown") || strings.HasSuffix(c, "|") {
			t.Fatalf("durations called for unknown/empty branch: %q", c)
		}
		if c == "api|tiny" {
			t.Fatalf("durations called for branch below minimum")
		}
	}
	if n := len(f.durationCalls()); n != 2 {
		t.Fatalf("want 2 durations calls, got %d: %v", n, f.durationCalls())
	}
}

func TestAuthOrder(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	wh := filepath.Join(dir, "wh")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(wh, 0o755)
	os.WriteFile(filepath.Join(home, ".wakatime.cfg"), []byte("[settings]\napi_key = home-key\n"), 0o600)

	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	b64 := func(s string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(s)) }

	a, err := resolveAuth(getenv, home)
	if err != nil || a.Header != b64("home-key") {
		t.Fatalf("home cfg: %v %q", err, a.Header)
	}

	os.WriteFile(filepath.Join(wh, ".wakatime.cfg"), []byte("[other]\napi_key = nope\n[settings]\napi_key=wh-key\n"), 0o600)
	env["WAKATIME_HOME"] = wh
	if a, _ = resolveAuth(getenv, home); a.Header != b64("wh-key") {
		t.Fatalf("WAKATIME_HOME cfg: %q", a.Header)
	}

	env["WAKATIME_API_KEY"] = "env-key"
	if a, _ = resolveAuth(getenv, home); a.Header != b64("env-key") {
		t.Fatalf("API key: %q", a.Header)
	}

	env["WAKATIME_ACCESS_TOKEN"] = "tok"
	if a, _ = resolveAuth(getenv, home); a.Header != "Bearer tok" {
		t.Fatalf("access token: %q", a.Header)
	}

	if _, err := resolveAuth(func(string) string { return "" }, filepath.Join(dir, "none")); err != errNoCredentials {
		t.Fatalf("want errNoCredentials, got %v", err)
	}
}

func TestAuthHeaderSent(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := testClient(srv.URL)
	c.Auth = Auth{Header: basic("k")}
	if _, err := c.Collect(Options{Date: testDate, Timezone: "UTC", MinMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	for _, h := range f.auths {
		if h != basic("k") {
			t.Fatalf("unexpected Authorization header %q", h)
		}
	}
}

func TestForbiddenStops(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := testClient(srv.URL).Collect(Options{Date: testDate, Timezone: "UTC", MinMinutes: 1})
	if err != ErrForbidden {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	if !strings.Contains(err.Error(), "read_heartbeats") || calls != 1 {
		t.Fatalf("bad error or calls=%d: %v", calls, err)
	}
}

func TestRetry429(t *testing.T) {
	f := newFake()
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		cur := n
		mu.Unlock()
		if cur <= 2 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if _, err := c.Collect(Options{Date: testDate, Timezone: "UTC", MinMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != 7*time.Second {
		t.Fatalf("unexpected sleeps %v", slept)
	}

	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer always.Close()
	c2 := testClient(always.URL)
	tries := 0
	c2.Sleep = func(time.Duration) { tries++ }
	if _, err := c2.Collect(Options{Date: testDate, Timezone: "UTC", MinMinutes: 1}); err == nil || tries != 3 {
		t.Fatalf("want failure after 3 retries, tries=%d err=%v", tries, err)
	}
}

func TestMCPFlow(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	var gotOpt Options
	collect := func(o Options) ([]Row, error) {
		gotOpt = o
		return testClient(srv.URL).Collect(o)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"wakatime_branch_durations","arguments":{"date":"2026-11-05","min_minutes":6}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"wakatime_branch_durations","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"x","method":"nope"}`,
		``,
	}, "\n")
	var out strings.Builder
	if err := serveMCP(strings.NewReader(input), &out, collect); err != nil {
		t.Fatal(err)
	}

	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		resps = append(resps, m)
	}
	if len(resps) != 6 {
		t.Fatalf("want 6 responses (notification ignored), got %d:\n%s", len(resps), out.String())
	}

	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion not echoed: %v", init["protocolVersion"])
	}
	if resps[1]["id"].(float64) != 2 || resps[1]["result"] == nil {
		t.Fatalf("bad ping: %v", resps[1])
	}
	tools := resps[2]["result"].(map[string]any)["tools"].([]any)
	tool := tools[0].(map[string]any)
	if len(tools) != 1 || tool["name"] != toolName {
		t.Fatalf("bad tools/list: %v", tools)
	}
	req := tool["inputSchema"].(map[string]any)["required"].([]any)
	if len(req) != 1 || req[0] != "date" {
		t.Fatalf("date must be required: %v", req)
	}

	call := resps[3]["result"].(map[string]any)
	if call["isError"] != false {
		t.Fatalf("tool call failed: %v", call)
	}
	text := call["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "project,branch,start,seconds\n") ||
		!strings.Contains(text, "api,feature/CAP-123-login,2026-11-05T09:00:00+01:00,720") {
		t.Fatalf("unexpected CSV:\n%s", text)
	}
	if strings.Contains(text, "web,") {
		t.Fatalf("min_minutes=6 should drop the 300 s branch:\n%s", text)
	}
	if gotOpt.Timezone != "Europe/Warsaw" || gotOpt.MinMinutes != 6 {
		t.Fatalf("bad options %+v", gotOpt)
	}

	if resps[4]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("missing date must be a tool error: %v", resps[4])
	}
	if resps[5]["error"].(map[string]any)["code"].(float64) != -32601 || resps[5]["id"] != "x" {
		t.Fatalf("bad unknown-method response: %v", resps[5])
	}
}
