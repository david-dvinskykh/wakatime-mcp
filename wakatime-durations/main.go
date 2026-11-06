// Command wakatime-durations reports WakaTime coding intervals per project
// and branch, either as CSV (with -date) or as an MCP server over stdio.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	defaultTimezone   = "Europe/Warsaw"
	defaultMinMinutes = 1.0
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("wakatime-durations", flag.ContinueOnError)
	date := fs.String("date", "", "day to report, YYYY-MM-DD; without it the binary serves MCP over stdio")
	tz := fs.String("timezone", defaultTimezone, "IANA timezone")
	minMinutes := fs.Float64("min-minutes", defaultMinMinutes, "skip branches with less total time")
	verbose := fs.Bool("v", false, "print the auth method (never the credential) to stderr")
	installCursorFlag := fs.Bool("install-cursor", false, "register this binary as an MCP server in Cursor's mcp.json and exit")
	cursorConfig := fs.String("cursor-config", "", "mcp.json to edit with -install-cursor (default ~/.cursor/mcp.json)")
	login := fs.Bool("login", false, "sign in to WakaTime with OAuth 2.0 in the browser, save the token and exit")
	logout := fs.Bool("logout", false, "delete the token saved by -login and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	home, _ := os.UserHomeDir()
	tokenPath := defaultTokenPath(os.Getenv)
	logins := &LoginManager{Config: oauthConfigFromEnv(os.Getenv, tokenPath)}

	if *login {
		p, err := logins.Start()
		if err != nil {
			fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "Opening the WakaTime sign-in page. If no browser opens, visit:\n"+p.url)
		if err := p.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "Signed in. Token saved to", tokenPath)
		return 0
	}
	if *logout {
		if err := deleteToken(tokenPath); err != nil {
			fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "Signed out. Deleted", tokenPath)
		return 0
	}

	if *installCursorFlag {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "wakatime-durations: cannot find own path:", err)
			return 1
		}
		cfg := *cursorConfig
		if cfg == "" {
			cfg = filepath.Join(home, ".cursor", "mcp.json")
		}
		if err := installCursor(cfg, exe); err != nil {
			fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
			return 1
		}
		fmt.Printf("Added %q to %s (command %s). Restart Cursor or refresh it in Settings > MCP.\n", cursorServerName, cfg, exe)
		return 0
	}
	collect := func(opt Options) ([]Row, error) {
		auth, err := resolveAuth(os.Getenv, home, tokenPath)
		if err != nil {
			return nil, err
		}
		if *verbose {
			fmt.Fprintln(os.Stderr, "wakatime-durations: auth method:", auth.Method)
		}
		return NewClient(auth).Collect(opt)
	}

	if *date == "" {
		srv := &Server{
			Collect: collect,
			Login: func() (string, error) {
				p, err := logins.Start()
				if err != nil {
					return "", err
				}
				return p.url, nil
			},
			HasCredentials: func() bool {
				_, err := resolveAuth(os.Getenv, home, tokenPath)
				return err == nil
			},
			Status: func() string {
				auth, err := resolveAuth(os.Getenv, home, tokenPath)
				if err != nil {
					return "Not signed in. " + err.Error()
				}
				return "Using " + auth.Method + "."
			},
			Logout: func() error { return deleteToken(tokenPath) },
			Notify: func(msg string) { fmt.Fprintln(os.Stderr, "wakatime-durations:", msg) },
		}
		if err := serveMCP(os.Stdin, os.Stdout, srv); err != nil {
			fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
			return 1
		}
		return 0
	}

	rows, err := collect(Options{Date: *date, Timezone: *tz, MinMinutes: *minMinutes})
	if err != nil {
		fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
		if errors.Is(err, ErrForbidden) {
			return 3
		}
		return 1
	}
	if err := writeCSV(os.Stdout, rows); err != nil {
		fmt.Fprintln(os.Stderr, "wakatime-durations:", err)
		return 1
	}
	return 0
}
