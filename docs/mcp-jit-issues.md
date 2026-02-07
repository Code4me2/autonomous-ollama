# MCP JIT Implementation - Issues Tracker

## Status: Active Development

This document tracks known issues with the unified MCP/JIT architecture.

Last updated: 2026-02-06

---

## Issue 1: Parser Inconsistency on First Request

**Status: ✅ FIXED** (commits da4db323, 8ee6c90c)

**Symptom:** First tool call sometimes outputs raw format instead of being parsed:
```
mcp_discover[ARGS]{"pattern": "*file*"}
```

**Root Cause:** Model may not output `[TOOL_CALLS]` prefix consistently.

**Solution Implemented:**
1. ✅ Added exact format specification to JIT context (da4db323)
2. ✅ Added `looksLikeFailedToolCall()` detection in execution loop (8ee6c90c)
3. ✅ Auto-retry with format correction when malformed tool call detected
4. ✅ Streaming suppression on retry rounds to hide failed attempts

**How it works:**
- Execution loop detects pattern `toolname[ARGS]{...}` in content
- Adds failed attempt to message history with correction prompt
- Retries with `suppressStreaming=true` to hide retry output
- Model receives: "Please use the exact format: [TOOL_CALLS]tool_name[ARGS]{...}"

---

## Issue 2: Tool Discovery Limit Too Restrictive

**Symptom:** Essential tools like `list_directory` and `search_files` are not discovered.

**Current Behavior:**
- `max_tools_per_discovery = 5` (default)
- Filesystem server has **14 tools** total
- Discovery returns first 5 matching tools alphabetically
- `list_directory` gets cut off

**Discovered tools (5):**
- `filesystem:read_file`
- `filesystem:read_text_file`
- `filesystem:read_media_file`
- `filesystem:read_multiple_files`
- `filesystem:write_file`

**Missing critical tools:**
- `filesystem:list_directory` - needed to list directory contents
- `filesystem:search_files` - needed to find files
- `filesystem:get_file_info` - needed for file metadata

**Result:** Model tries to use `read_multiple_files` to list a directory, which fails.

**Potential Fixes:**
1. Increase default limit to 10-15
2. Prioritize "essential" tools (list, search) in discovery
3. Allow multiple discovery calls with different patterns
4. Sort tools by relevance/usefulness rather than alphabetically

---

## Issue 3: Model Uses Wrong Tools for Task

**Symptom:** Model attempts directory listing with file read tools.

**Log evidence:**
```
Tool call details round=1 name=filesystem:read_multiple_files arguments={paths: [/home/velvetm]}
Result: EACCES: permission denied, open '/home/velvetm'

Tool call details round=2 name=filesystem:read_multiple_files arguments={paths: [/home/velvetm/*]}
Result: ENOENT: no such file or directory, open '/home/velvetm/*'
```

**Root Cause:**
- `list_directory` tool not in discovered set
- Model doesn't know to do second discovery with `*list*` pattern
- Model improvises with available tools

**Potential Fixes:**
1. Include `list_directory` in initial discovery (see Issue 2)
2. Add guidance in JIT context: "If you need to list files, discover with `*list*` pattern"
3. Implement tool suggestions based on task type

---

## Issue 4: Raw Tool Call Format Leaking to Output

**Status: ✅ PARTIALLY FIXED** (commit 8ee6c90c)

**Symptom:** After tool execution, raw format appears in response.

**Solution Implemented:**
1. ✅ Added `suppressStreaming` parameter to `executeCompletionWithTools`
2. ✅ Track `retryingFailedToolCall` state in execution loop
3. ✅ Suppress streaming output during retry rounds

**Current behavior:**
- First malformed attempt may still be visible (streamed before detection)
- Subsequent retry attempts are fully suppressed
- Clean output on successful tool calls

**Remaining work (optional):**
- Could buffer first response to suppress initial failed attempt
- Would require changes to streaming architecture

---

## Issue 5: No Automatic Multi-Pattern Discovery

**Status: ✅ ADDRESSED** (commit da4db323)

**Symptom:** User asks to "list files and read them" but model only discovers read tools.

**Solution Implemented:**
1. ✅ Added multi-step guidance in JIT context
2. ✅ Context now instructs: "If a task involves multiple types of operations, call mcp_discover multiple times with different patterns"
3. ✅ Provides workflow example showing sequential discovery

**JIT context now includes:**
```
MULTI-STEP TASKS: If a task involves multiple types of operations
(e.g., "list files and read them"), call mcp_discover multiple times
with different patterns to find all needed tools BEFORE attempting
the operations.

Common patterns: "*file*", "*directory*", "*list*", "*search*", "*git*"
```

---

## Configuration Reference

Current defaults in `mcp_manager.go`:
```go
maxToolsPerDiscovery = 5  // Consider increasing to 10-15
maxClients = 10
```

JIT context injection in `mcp_code_api.go`:
```go
// Current prompt may need enhancement for format consistency
context.WriteString(`You have access to external tools via MCP...`)
```

---

## New Feature: OpenAI Endpoint MCP Support

**Status: ✅ IMPLEMENTED** (commit d614b4ce)

MCP is now available via the OpenAI compatibility endpoint `/v1/chat/completions`:

```json
{
  "model": "ministral-3:14b",
  "messages": [...],
  "mcp_servers": [
    {
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"]
    }
  ],
  "tools_path": "/home/user/Desktop",
  "jit_max_tools": 10
}
```

---

## New Feature: WebSocket Transport for Remote MCP Servers

**Status: ✅ IMPLEMENTED** (commit e2a72140)

MCP servers can now be accessed remotely via WebSocket transport. This enables:
- Running MCP servers on remote machines (e.g., over Tailscale)
- Persistent bidirectional connections with low latency
- Secure connections with header-based authentication

**Configuration:**
```json
{
  "mcp_servers": [
    {
      "name": "remote-server",
      "transport": "websocket",
      "url": "ws://server.tailnet.ts.net:8080/mcp",
      "headers": {
        "Authorization": "Bearer your-token"
      }
    }
  ]
}
```

**Transport options:**
- `stdio` (default): Local process via stdin/stdout
- `websocket`: Remote server via WebSocket

**Files added:**
- `server/mcp_client_interface.go` - Transport abstraction interface
- `server/mcp_client_ws.go` - WebSocket client implementation

**Benefits for Tailscale:**
- WireGuard encryption at network layer
- No additional TLS required within tailnet
- Sub-millisecond latency within network

---

## Testing Checklist

- [x] First request parses tool call correctly (with auto-retry)
- [ ] Discovery returns essential tools (list, search, read, write)
- [ ] Model uses correct tools for directory listing
- [x] No raw tool format in output (suppressed on retry)
- [x] Multi-round tool usage works
- [x] Discovery results persist across rounds
- [x] OpenAI endpoint supports MCP parameters

---

## Priority Order (Updated)

1. **High:** Issue 2 (tool limit) - blocking basic functionality
2. **High:** Issue 3 (wrong tools) - consequence of Issue 2
3. ~~**Medium:** Issue 1 (parser inconsistency)~~ ✅ FIXED
4. ~~**Medium:** Issue 4 (format leaking)~~ ✅ PARTIALLY FIXED
5. ~~**Low:** Issue 5 (multi-pattern)~~ ✅ ADDRESSED
