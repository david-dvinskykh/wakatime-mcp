package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the only host this tool talks to.
const DefaultBaseURL = "https://wakatime.com/api/v1/users/current"

const (
	requestTimeout = 45 * time.Second
	maxRetries429  = 3
	mergeGap       = 60.0 // seconds
)

// ErrForbidden signals a 401 or 403 from WakaTime.
var ErrForbidden = errors.New("WakaTime refused the request (401/403): an OAuth token with the read_heartbeats scope is needed; set WAKATIME_ACCESS_TOKEN")

// Client is a minimal WakaTime API client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Auth    Auth
	// Sleep is used for 429 back-off; replaceable in tests.
	Sleep func(time.Duration)
}

// NewClient returns a client with the default base URL and timeout.
func NewClient(auth Auth) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: requestTimeout},
		Auth:    auth,
		Sleep:   time.Sleep,
	}
}

// Row is one output line.
type Row struct {
	Project string
	Branch  string
	Start   string // RFC3339 in the requested timezone, or empty
	Seconds int64
	startT  time.Time
}

type summariesResp struct {
	Data []struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
		Branches []struct {
			Name         string  `json:"name"`
			TotalSeconds float64 `json:"total_seconds"`
		} `json:"branches"`
	} `json:"data"`
}

type durationsResp struct {
	Data []struct {
		Time     float64 `json:"time"`
		Duration float64 `json:"duration"`
	} `json:"data"`
}

func (c *Client) get(path string, q url.Values, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path + "?" + q.Encode()
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.Auth.Header)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "wakatime-durations/1.0")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return ErrForbidden
		case resp.StatusCode == http.StatusTooManyRequests:
			if attempt >= maxRetries429 {
				return fmt.Errorf("GET %s: rate limited (429) after %d retries", path, maxRetries429)
			}
			sleep(retryAfter(resp.Header.Get("Retry-After"), attempt))
			continue
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		if readErr != nil {
			return fmt.Errorf("GET %s: read body: %w", path, readErr)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("GET %s: decode: %w", path, err)
		}
		return nil
	}
}

// retryAfter parses a Retry-After header (seconds or HTTP date) and falls
// back to exponential back-off.
func retryAfter(h string, attempt int) time.Duration {
	h = strings.TrimSpace(h)
	if h != "" {
		if s, err := strconv.Atoi(h); err == nil && s >= 0 {
			return time.Duration(s) * time.Second
		}
		if t, err := http.ParseTime(h); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
			return 0
		}
	}
	return time.Duration(1<<attempt) * 2 * time.Second
}

// Options configure one collection run.
type Options struct {
	Date       string
	Timezone   string
	MinMinutes float64
}

func isUnknownBranch(name string) bool {
	n := strings.TrimSpace(name)
	return n == "" || strings.EqualFold(n, "unknown")
}

// Collect fetches projects, branches and durations and returns sorted rows.
func (c *Client) Collect(opt Options) ([]Row, error) {
	if _, err := time.Parse("2006-01-02", opt.Date); err != nil {
		return nil, fmt.Errorf("invalid date %q: want YYYY-MM-DD", opt.Date)
	}
	loc, err := time.LoadLocation(opt.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", opt.Timezone, err)
	}
	minSeconds := opt.MinMinutes * 60

	base := url.Values{}
	base.Set("start", opt.Date)
	base.Set("end", opt.Date)
	base.Set("timezone", opt.Timezone)

	var all summariesResp
	if err := c.get("/summaries", base, &all); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var projects []string
	for _, d := range all.Data {
		for _, p := range d.Projects {
			if p.Name != "" && !seen[p.Name] {
				seen[p.Name] = true
				projects = append(projects, p.Name)
			}
		}
	}
	sort.Strings(projects)

	var rows []Row
	for _, project := range projects {
		q := cloneValues(base)
		q.Set("project", project)
		var ps summariesResp
		if err := c.get("/summaries", q, &ps); err != nil {
			return nil, err
		}
		totals := map[string]float64{}
		var order []string
		for _, d := range ps.Data {
			for _, b := range d.Branches {
				if _, ok := totals[b.Name]; !ok {
					order = append(order, b.Name)
				}
				totals[b.Name] += b.TotalSeconds
			}
		}
		for _, branch := range order {
			total := totals[branch]
			if total < minSeconds {
				continue
			}
			if isUnknownBranch(branch) {
				rows = append(rows, Row{Project: project, Branch: branch, Seconds: roundSec(total)})
				continue
			}
			dq := url.Values{}
			dq.Set("date", opt.Date)
			dq.Set("timezone", opt.Timezone)
			dq.Set("project", project)
			dq.Set("branches", branch)
			var dr durationsResp
			if err := c.get("/durations", dq, &dr); err != nil {
				return nil, err
			}
			blocks := make([]block, 0, len(dr.Data))
			for _, it := range dr.Data {
				blocks = append(blocks, block{start: it.Time, end: it.Time + it.Duration})
			}
			for _, b := range mergeBlocks(blocks, mergeGap) {
				t := unixToTime(b.start).In(loc)
				rows = append(rows, Row{
					Project: project,
					Branch:  branch,
					Start:   t.Format(time.RFC3339),
					Seconds: roundSec(b.end - b.start),
					startT:  t,
				})
			}
		}
	}
	sortRows(rows)
	return rows, nil
}

type block struct{ start, end float64 }

// mergeBlocks merges blocks whose gap is at most maxGap seconds.
func mergeBlocks(in []block, maxGap float64) []block {
	if len(in) == 0 {
		return nil
	}
	bs := append([]block(nil), in...)
	sort.Slice(bs, func(i, j int) bool { return bs[i].start < bs[j].start })
	out := []block{bs[0]}
	for _, b := range bs[1:] {
		last := &out[len(out)-1]
		if b.start-last.end <= maxGap {
			if b.end > last.end {
				last.end = b.end
			}
			continue
		}
		out = append(out, b)
	}
	return out
}

// sortRows orders rows by start time; rows without a start go last.
func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Start == "") != (b.Start == "") {
			return a.Start != ""
		}
		if a.Start != "" && !a.startT.Equal(b.startT) {
			return a.startT.Before(b.startT)
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Branch < b.Branch
	})
}

func unixToTime(sec float64) time.Time {
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9))
}

func roundSec(s float64) int64 { return int64(math.Round(s)) }

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
