package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

const toolName = "wakatime_branch_durations"

// Collector abstracts data collection so the MCP layer can be tested.
type Collector func(Options) ([]Row, error)

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
func serveMCP(r io.Reader, w io.Writer, collect Collector) error {
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
		resp := handle(req, collect)
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

func handle(req rpcRequest, collect Collector) rpcResponse {
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
			"serverInfo":      map[string]any{"name": "wakatime-durations", "version": "1.0.0"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": []any{toolDefinition()}}
	case "tools/call":
		var p struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params"}
			return resp
		}
		if p.Name != toolName {
			resp.Error = &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
			return resp
		}
		opt, err := parseToolArgs(p.Arguments)
		if err != nil {
			resp.Result = toolText(err.Error(), true)
			return resp
		}
		rows, err := collect(opt)
		if err != nil {
			resp.Result = toolText(err.Error(), true)
			return resp
		}
		out, err := csvString(rows)
		if err != nil {
			resp.Result = toolText(err.Error(), true)
			return resp
		}
		resp.Result = toolText(out, false)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}

func toolDefinition() map[string]any {
	return map[string]any{
		"name": toolName,
		"description": "WakaTime coding intervals for one day, per project and branch, as CSV " +
			"(project,branch,start,seconds). Blocks on the same branch at most 60 s apart are merged. " +
			"Branch Unknown or empty yields one row with an empty start and the summary total.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"date":        map[string]any{"type": "string", "description": "Day in YYYY-MM-DD"},
				"timezone":    map[string]any{"type": "string", "description": "IANA timezone, default Europe/Warsaw"},
				"min_minutes": map[string]any{"type": "number", "description": "Skip branches with less total time, default 1"},
			},
			"required": []string{"date"},
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
