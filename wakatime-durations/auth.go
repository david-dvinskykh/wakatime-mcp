package main

import (
	"bufio"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Auth holds the Authorization header value and a human-readable name of
// the method that produced it. The header value must never be logged.
type Auth struct {
	Header string
	Method string
}

// errNoCredentials is returned when no credential source is available.
var errNoCredentials = errors.New("no WakaTime credentials found: set WAKATIME_ACCESS_TOKEN (OAuth token with read_heartbeats scope), WAKATIME_API_KEY, or api_key in ~/.wakatime.cfg")

// resolveAuth picks credentials in this order:
//  1. WAKATIME_ACCESS_TOKEN as Bearer
//  2. WAKATIME_API_KEY as Basic
//  3. api_key from $WAKATIME_HOME/.wakatime.cfg or ~/.wakatime.cfg as Basic
func resolveAuth(getenv func(string) string, homeDir string) (Auth, error) {
	if tok := strings.TrimSpace(getenv("WAKATIME_ACCESS_TOKEN")); tok != "" {
		return Auth{Header: "Bearer " + tok, Method: "WAKATIME_ACCESS_TOKEN (Bearer)"}, nil
	}
	if key := strings.TrimSpace(getenv("WAKATIME_API_KEY")); key != "" {
		return Auth{Header: basic(key), Method: "WAKATIME_API_KEY (Basic)"}, nil
	}
	var candidates []string
	if wh := strings.TrimSpace(getenv("WAKATIME_HOME")); wh != "" {
		candidates = append(candidates, filepath.Join(wh, ".wakatime.cfg"))
	}
	if homeDir != "" {
		candidates = append(candidates, filepath.Join(homeDir, ".wakatime.cfg"))
	}
	for _, path := range candidates {
		key, err := readConfigAPIKey(path)
		if err == nil && key != "" {
			return Auth{Header: basic(key), Method: "api_key from " + path + " (Basic)"}, nil
		}
	}
	return Auth{}, errNoCredentials
}

func basic(key string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(key))
}

// readConfigAPIKey reads api_key from the [settings] section of a
// wakatime.cfg INI file.
func readConfigAPIKey(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "settings" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(strings.ToLower(k)) == "api_key" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", sc.Err()
}
