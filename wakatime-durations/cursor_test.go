package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCursorKeepsOtherServers(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".cursor", "mcp.json")

	// Fresh install creates the file and directory.
	if err := installCursor(cfg, "/bin/first"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("no backup expected for a new file")
	}

	existing := `{"mcpServers":{"other":{"url":"https://example.invalid/mcp"}},"keep":true}`
	if err := os.WriteFile(cfg, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installCursor(cfg, "/usr/local/bin/wakatime-durations"); err != nil {
		t.Fatal(err)
	}
	// Running twice must be idempotent.
	if err := installCursor(cfg, "/usr/local/bin/wakatime-durations"); err != nil {
		t.Fatal(err)
	}

	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
		Keep       bool                      `json:"keep"`
	}
	data, _ := os.ReadFile(cfg)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON written: %v\n%s", err, data)
	}
	if !got.Keep || got.MCPServers["other"]["url"] != "https://example.invalid/mcp" {
		t.Fatalf("existing config lost:\n%s", data)
	}
	if got.MCPServers[cursorServerName]["command"] != "/usr/local/bin/wakatime-durations" || len(got.MCPServers) != 2 {
		t.Fatalf("bad entry:\n%s", data)
	}
	if _, err := os.Stat(cfg + ".bak"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestInstallCursorRefusesInvalidJSON(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	os.WriteFile(cfg, []byte("{not json"), 0o600)
	if err := installCursor(cfg, "/x"); err == nil {
		t.Fatal("want error for invalid JSON")
	}
	if b, _ := os.ReadFile(cfg); string(b) != "{not json" {
		t.Fatal("invalid file must be left unchanged")
	}
}
