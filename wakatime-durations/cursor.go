package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const cursorServerName = "wakatime-durations"

// installCursor adds or updates the wakatime-durations entry in a Cursor
// mcp.json file, keeping every other server and top-level key. The previous
// file, if any, is saved next to it with a .bak suffix.
func installCursor(configPath, command string) error {
	root := map[string]json.RawMessage{}
	old, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		if len(old) > 0 {
			if err := json.Unmarshal(old, &root); err != nil {
				return fmt.Errorf("%s is not valid JSON, left unchanged: %w", configPath, err)
			}
		}
	case os.IsNotExist(err):
		old = nil
	default:
		return err
	}

	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("mcpServers in %s is not an object, left unchanged: %w", configPath, err)
		}
	}
	entry, err := json.Marshal(map[string]any{"command": command, "args": []string{}})
	if err != nil {
		return err
	}
	servers[cursorServerName] = entry
	if root["mcpServers"], err = json.Marshal(servers); err != nil {
		return err
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if old != nil {
		if err := os.WriteFile(configPath+".bak", old, 0o600); err != nil {
			return fmt.Errorf("backup failed, %s left unchanged: %w", configPath, err)
		}
	}
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath)
}
