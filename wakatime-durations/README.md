# wakatime-durations

WakaTime coding intervals with start times, per project and branch, so time
can be assigned to Jira tickets. Standard library only.

The binary has two modes:

- `-date YYYY-MM-DD` prints CSV (`project,branch,start,seconds`).
  Optional `-timezone` (default `Europe/Warsaw`), `-min-minutes` (default `1`)
  and `-v` (prints which auth method was used, never the credential).
- No `-date`: an MCP server over stdio (newline-delimited JSON-RPC) with one
  tool, `wakatime_branch_durations` (`date` required, `timezone`, `min_minutes`).

Blocks on the same branch at most 60 s apart are merged. A branch named
`Unknown` or an empty branch gives one row with an empty `start` and the
summary total. Rows are sorted by start; rows without a start come last.

## Add to Cursor with npx (recommended)

Add this to `~/.cursor/mcp.json` under `mcpServers` (or to `.cursor/mcp.json`
in a project):

```json
"wakatime-durations": {
  "command": "npx",
  "args": ["-y", "wakatime-durations-mcp"]
}
```

Nothing else to install besides Node. On first start the launcher downloads
the binary for your OS and CPU from this repository's GitHub release, checks
its SHA-256 against `checksums.txt` and caches it
(`~/Library/Caches/wakatime-durations/<version>/` on macOS). Later starts use
the cache. If no release binary is available but Go is installed, it builds the
bundled source instead. Its own messages go to stderr; stdout is MCP only.

CLI mode works the same way:
`npx -y wakatime-durations-mcp -date 2026-11-06`.

Launcher overrides: `WAKATIME_DURATIONS_BIN` (use this binary),
`WAKATIME_DURATIONS_CACHE` (cache directory).

### Publishing a version (maintainer)

1. Set `version` in `package.json` (for example `0.1.0`).
2. Push a matching tag: `git tag v0.1.0 && git push origin v0.1.0`.
3. The `release` workflow tests, builds binaries for macOS (arm64, amd64),
   Linux (amd64, arm64) and Windows (amd64), creates the GitHub release with
   `checksums.txt` and runs `npm publish`. The npm step needs an `NPM_TOKEN`
   repository secret (an npm automation token); without it the step is
   skipped and only the GitHub release is made.

## Install and add to Cursor in one command (Go)

Needs Go 1.22+ (`brew install go`). This downloads, builds and installs the
binary, then registers it in `~/.cursor/mcp.json` (other servers are kept and
the old file is saved as `mcp.json.bak`):

```bash
GOBIN="$HOME/.local/bin" go install github.com/david-dvinskykh/wakatime-mcp/wakatime-durations@latest && "$HOME/.local/bin/wakatime-durations" -install-cursor
```

Restart Cursor (or refresh it in **Settings → MCP**). That is all; see
[Credentials](#credentials) for which key it uses.

Run the same command again to update. `@latest` works once this directory is
on the repository's default branch (or a `wakatime-durations/vX.Y.Z` tag
exists); before that, use [Build from source](#build-from-source).

### npx-style alternative (no install step)

Like `npx`, Cursor can fetch and run it on start with `go run`. Add this to
`~/.cursor/mcp.json` under `mcpServers`:

```json
"wakatime-durations": {
  "command": "go",
  "args": ["run", "github.com/david-dvinskykh/wakatime-mcp/wakatime-durations@latest"]
}
```

The first start compiles it (a few seconds); later starts use Go's cache.

## Build from source

```bash
cd ~/wakatime-durations
go vet ./... && go test ./...
go build -trimpath -o ~/.local/bin/wakatime-durations .
~/.local/bin/wakatime-durations -install-cursor
```

## Sign in with OAuth 2.0 (interactive)

Cursor's own **Login** button only works for remote (`url`) MCP servers whose
provider publishes OAuth discovery metadata; WakaTime publishes none, and this
server runs over stdio. So the server signs you in itself, using the OAuth 2.0
authorization-code flow with a redirect to `127.0.0.1`:

- **Right after you save `mcp.json`.** Cursor starts the server; if it finds no
  credentials, it opens the WakaTime consent page in your browser. Click
  **Authorize** and the token is saved. If no browser opens, the link is in
  **View → Output → MCP Logs**.
- **On a tool call while not signed in** (or after a 401/403),
  `wakatime_branch_durations` answers with the sign-in link instead of data.
  Authorize, then ask again.
- **Any time:** the `wakatime_login`, `wakatime_auth_status` and
  `wakatime_logout` tools, or `wakatime-durations -login` / `-logout` in a
  terminal.

### One-time setup

1. Create an app at https://wakatime.com/apps with redirect URI
   `http://127.0.0.1:8977/callback`. If the form rejects it, use
   `http://localhost:8977/callback` and set
   `WAKATIME_OAUTH_REDIRECT_URI` to the same value.
2. Put the app's ID and secret into the server entry in the global
   `~/.cursor/mcp.json` (not a project's `.cursor/mcp.json`, which may be
   committed):

   ```json
   "wakatime-durations": {
     "command": "go",
     "args": ["run", "github.com/david-dvinskykh/wakatime-mcp/wakatime-durations@latest"],
     "env": {
       "GOPROXY": "direct",
       "WAKATIME_CLIENT_ID": "your-app-id",
       "WAKATIME_CLIENT_SECRET": "your-app-secret"
     }
   }
   ```

   To keep the secret out of `mcp.json`, put the two `WAKATIME_CLIENT_*` lines
   in a file and point `"envFile"` to its absolute path instead.
3. Save. The browser opens; click **Authorize**. Check in chat with
   `wakatime_auth_status`.

The token is stored in
`~/Library/Application Support/wakatime-durations/token.json` on macOS
(`~/.config/wakatime-durations/token.json` on Linux), readable only by you.
Override with `WAKATIME_TOKEN_FILE`. Requested scopes:
`read_summaries,read_heartbeats`. Set `WAKATIME_NO_BROWSER=1` to only print the
link.

## Credentials

Checked in this order:

1. `WAKATIME_ACCESS_TOKEN` – OAuth token, sent as `Authorization: Bearer`.
2. `WAKATIME_API_KEY` – sent as `Authorization: Basic base64(key)`.
3. The token saved by the interactive sign-in – sent as Bearer.
4. `api_key` in the `[settings]` section of `$WAKATIME_HOME/.wakatime.cfg`,
   then `~/.wakatime.cfg` – sent as Basic.

If WakaTime answers 401 or 403, the CLI stops (exit code 3); the MCP tool
returns a sign-in link.

## CLI usage

```bash
wakatime-durations -date "$(date +%F)" -timezone Europe/Warsaw | head -11
```

## Add to Cursor by hand

Only needed if you don't use `-install-cursor`. Cursor starts MCP servers
itself, so give it the absolute path to the binary
(Cursor does not expand `~`). Find it with:

```bash
echo "$HOME/.local/bin/wakatime-durations"
```

1. Open **Cursor → Settings → Cursor Settings → MCP → Add new global MCP
   server**. This opens `~/.cursor/mcp.json`. For a single project, use
   `.cursor/mcp.json` in that project instead.
2. Add an entry under `mcpServers`, keeping any servers already there:

   ```json
   {
     "mcpServers": {
       "wakatime-durations": {
         "command": "/Users/YOUR_USER/.local/bin/wakatime-durations",
         "args": []
       }
     }
   }
   ```

   No `-date` argument: without it the binary runs as an MCP server.

3. Credentials. Cursor opened from the Dock does not see variables exported
   in `~/.zshrc`, so pick one:
   - **Simplest:** keep `api_key` in `~/.wakatime.cfg` (the WakaTime editor
     plugin usually creates it). The binary reads the file itself; nothing
     else to set.
   - **OAuth token:** pass it from your environment instead of writing it
     into the file:

     ```json
     "env": { "WAKATIME_ACCESS_TOKEN": "${env:WAKATIME_ACCESS_TOKEN}" }
     ```

     This only works if Cursor was started from a shell where the variable is
     exported (for example `open -a Cursor` from Terminal). Do not paste the
     token value itself into a project's `.cursor/mcp.json`, since that file
     may get committed.
4. Save, then go back to **Settings → MCP**. `wakatime-durations` should show a
   green dot and one tool, `wakatime_branch_durations`. If it is red, click the
   refresh icon. **View → Output → MCP Logs** shows the server's stderr.
5. In Cursor chat (Agent mode), ask for example: *"Use
   wakatime_branch_durations for 2026-11-06 and group the time by Jira key in
   the branch name."*

### Check the server by hand

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | ~/.local/bin/wakatime-durations
```

You should get two JSON lines: the `initialize` result and a tool list with
`wakatime_branch_durations`.
