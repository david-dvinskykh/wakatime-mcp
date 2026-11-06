# wakatime-mcp

## wakatime-durations

WakaTime coding intervals with start times, per project and branch, as CSV or
as an MCP server. Add it to Cursor (`~/.cursor/mcp.json`, under `mcpServers`):

```json
"wakatime-durations": {
  "command": "npx",
  "args": ["-y", "wakatime-durations-mcp"]
}
```

Details, credentials and other install options:
[wakatime-durations/README.md](wakatime-durations/README.md).
