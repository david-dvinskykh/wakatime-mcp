package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

const (
	toolName       = "wakatime_branch_durations"
	toolLogin      = "wakatime_login"
	toolAuthStatus = "wakatime_auth_status"
	toolLogout     = "wakatime_logout"
)

// Collector abstracts data collection so the MCP layer can be tested.
type Collector func(Options) ([]Row, error)

// Server holds the operations the MCP tools expose.
type Server struct {
	Collect Collector
	// Login starts (or joins) the browser sign-in and returns its URL.
	Login func() (string, error)
	// HasCredentials reports whether any credential source is available.
	HasCredentials func() bool
	// Status describes the credential in use, never its value.
	Status func() string
	// Logout removes the token saved by Login.
	Logout func() error
	// Notify receives human-readable messages for the client's log (stderr).
	Notify func(string)

	autoOnce sync.Once
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// serveMCP runs a newline-delimited JSON-RPC MCP server over r/w.
func serveMCP(r io.Reader, w io.Writer, s *Server) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if err := enc.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		// Notifications carry no id and get no response.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		resp := s.handle(req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}

func (s *Server) notify(msg string) {
	if s.Notify != nil {
		s.Notify(msg)
	}
}

// autoLogin starts the browser sign-in once, when the client connects and
// no credentials exist, so signing in follows right after adding the server.
func (s *Server) autoLogin() {
	s.autoOnce.Do(func() {
		if s.HasCredentials == nil || s.Login == nil || s.HasCredentials() {
			return
		}
		u, err := s.Login()
		if err != nil {
			if !errors.Is(err, errOAuthNotConfigured) {
				s.notify("automatic sign-in not started: " + err.Error())
			}
			return
		}
		s.notify("not signed in; opened the WakaTime sign-in page. If no browser opened, visit: " + u)
	})
}

func (s *Server) handle(req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		resp.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "wakatime-durations", "version": "1.1.0"},
		}
		go s.autoLogin()
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolDefinitions()}
	case "tools/call":
		var p struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params"}
			return resp
		}
		switch p.Name {
		case toolName:
			resp.Result = s.callDurations(p.Arguments)
		case toolLogin:
			resp.Result = s.callLogin()
		case toolAuthStatus:
			resp.Result = toolText(s.status(), false)
		case toolLogout:
			if s.Logout == nil {
				resp.Result = toolText("logout is not available", true)
			} else if err := s.Logout(); err != nil {
				resp.Result = toolText("logout failed: "+err.Error(), true)
			} else {
				resp.Result = toolText("Signed out: the saved WakaTime sign-in token was deleted. "+s.status(), false)
			}
		default:
			resp.Error = &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func (s *Server) status() string {
	if s.Status == nil {
		return "status unavailable"
	}
	return s.Status()
}

func (s *Server) callDurations(args map[string]json.RawMessage) map[string]any {
	opt, err := parseToolArgs(args)
	if err != nil {
		return toolText(err.Error(), true)
	}
	rows, err := s.Collect(opt)
	if err != nil {
		if errors.Is(err, errNoCredentials) || errors.Is(err, ErrForbidden) {
			return s.loginPrompt(err)
		}
		return toolText(err.Error(), true)
	}
	out, err := csvString(rows)
	if err != nil {
		return toolText(err.Error(), true)
	}
	return toolText(out, false)
}

// loginPrompt turns an auth failure into a result that carries the sign-in
// link, so the user can authorize and simply retry.
func (s *Server) loginPrompt(cause error) map[string]any {
	if s.Login == nil {
		return toolText(cause.Error(), true)
	}
	u, err := s.Login()
	if err != nil {
		return toolText(cause.Error()+"\n\n"+err.Error(), true)
	}
	return toolText(fmt.Sprintf("Not signed in to WakaTime (%s).\n\n"+
		"Sign in here: %s\n\nA browser tab was opened for it. After you authorize, call %s again.",
		cause.Error(), u, toolName), true)
}

func (s *Server) callLogin() map[string]any {
	if s.Login == nil {
		return toolText("sign-in is not available", true)
	}
	u, err := s.Login()
	if err != nil {
		return toolText(err.Error(), true)
	}
	return toolText("Open this link to sign in to WakaTime (a browser tab was opened for it): "+u+
		"\n\nAfter you authorize, the token is saved and "+toolName+" works.", false)
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}

func toolDefinitions() []any {
	empty := map[string]any{"type": "object", "properties": map[string]any{}}
	return []any{
		map[string]any{
			"name": toolName,
			"description": "WakaTime coding intervals for one day, per project and branch, as CSV " +
				"(project,branch,start,seconds). Blocks on the same branch at most 60 s apart are merged. " +
				"Branch Unknown or empty yields one row with an empty start and the summary total. " +
				"If not signed in, returns a WakaTime sign-in link.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"date":        map[string]any{"type": "string", "description": "Day in YYYY-MM-DD"},
					"timezone":    map[string]any{"type": "string", "description": "IANA timezone, default Europe/Warsaw"},
					"min_minutes": map[string]any{"type": "number", "description": "Skip branches with less total time, default 1"},
				},
				"required": []string{"date"},
			},
		},
		map[string]any{
			"name":        toolLogin,
			"description": "Sign in to WakaTime with OAuth 2.0 in the browser. Returns the sign-in link; the token is saved locally when the user authorizes.",
			"inputSchema": empty,
		},
		map[string]any{
			"name":        toolAuthStatus,
			"description": "Show which WakaTime credential source is in use (never the secret itself).",
			"inputSchema": empty,
		},
		map[string]any{
			"name":        toolLogout,
			"description": "Delete the WakaTime token saved by " + toolLogin + ".",
			"inputSchema": empty,
		},
	}
}

func parseToolArgs(args map[string]json.RawMessage) (Options, error) {
	opt := Options{Timezone: defaultTimezone, MinMinutes: defaultMinMinutes}
	raw, ok := args["date"]
	if !ok || json.Unmarshal(raw, &opt.Date) != nil || opt.Date == "" {
		return opt, fmt.Errorf("date is required (YYYY-MM-DD)")
	}
	if raw, ok := args["timezone"]; ok {
		var tz string
		if err := json.Unmarshal(raw, &tz); err != nil {
			return opt, fmt.Errorf("timezone must be a string")
		}
		if tz != "" {
			opt.Timezone = tz
		}
	}
	if raw, ok := args["min_minutes"]; ok && string(raw) != "null" {
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return opt, fmt.Errorf("min_minutes must be a number")
			}
			if f, err = strconv.ParseFloat(s, 64); err != nil {
				return opt, fmt.Errorf("min_minutes must be a number")
			}
		}
		opt.MinMinutes = f
	}
	return opt, nil
}
