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

## Install and add to Cursor in one command

Needs Go 1.22+ (`brew install go`). This downloads, builds and installs the
binary, then registers it in `~/.cursor/mcp.json` (other servers are kept and
the old file is saved as `mcp.json.bak`):

```bash
GOBIN="$HOME/.local/bin" go install github.com/david-dvinskykh/wakatime-mcp/wakatime-durations@latest && "$HOME/.local/bin/wakatime-durations" -install-cursor
```

Restart Cursor (or refresh it in **Settings → MCP**). That is all; see
[Credentials](#credentials) for which key it uses.

Run the same command again to update. Until this code is merged into the
default branch, replace `@latest` with `@claude/wakatime-durations-tool-a8azah`.

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

## Credentials

Checked in this order:

1. `WAKATIME_ACCESS_TOKEN` – OAuth token, sent as `Authorization: Bearer`.
2. `WAKATIME_API_KEY` – sent as `Authorization: Basic base64(key)`.
3. `api_key` in the `[settings]` section of `$WAKATIME_HOME/.wakatime.cfg`,
   then `~/.wakatime.cfg` – sent as Basic.

If WakaTime answers 401 or 403, the tool stops (exit code 3) and says that an
OAuth token with the `read_heartbeats` scope is needed.

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
