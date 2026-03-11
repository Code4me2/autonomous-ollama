# Ollama MCP Fork — Development Context

You are working on a custom fork of Ollama with native MCP (Model Context Protocol) support. This fork enables language models to autonomously discover and execute tools via MCP servers within a single API request, using two-tier JIT (Just-In-Time) tool discovery.

**Repository**: `/home/velvetm/Desktop/ollama/`
**Language**: Go
**Port**: 11434

## Architecture Overview

The fork adds a complete MCP subsystem to Ollama's chat handler. When a model receives `mcp_servers` in its chat request, Ollama:
1. Creates/reuses an `MCPManager` for the session
2. Injects `mcp_discover` and `mcp_list_tools` built-in tools so the model can find available tools
3. **Tier 1** — Intercepts `mcp_discover` calls: returns a server catalog (name, description, tool count) — no tool schemas injected
4. **Tier 2** — Intercepts `mcp_list_tools` calls: loads tool schemas from requested servers, injects them for next round
5. Executes discovered tool calls against MCP servers (stdio or HTTP transport)
6. Injects tool results back into the conversation and continues generation
7. Loops until the model produces a final text response (up to `max_tool_rounds`)

## Two-Tier Discovery Flow

The model workflow is:
```
mcp_discover({"pattern": "*"})     → Server catalog (names + descriptions + tool counts)
mcp_list_tools({"servers": ["X"]}) → Tool schemas from server X injected
X:tool_name({"arg": "value"})      → Actual tool execution
```

**Design decisions**:
- Always two-tier (no fallback to flat tool list) — consistent model workflow
- `jit_max_tools` applies per-server in `HandleListTools` (3 servers × 5 = up to 15 tools)
- Description fallback chain: `MCPServerConfig.Description` → `GetServerDescription()` (from MCP initialize handshake) → `"(no description)"`
- `HandleDiscovery` returns `(string, error)` — catalog only, no tool schemas
- `HandleListTools` returns `([]api.Tool, string, error)` — tool schemas + summary
- Pattern matching in `mcp_discover` matches against server names (not tool names)

## Key Files

### MCP Core (`server/`)
| File | Purpose |
|------|---------|
| `mcp.go` | Public API: `GetMCPManager()`, `ResolveServersForRequest()`, `ListMCPServers()` |
| `mcp_manager.go` | Multi-client orchestration: `MCPManager`, `HandleDiscovery()` (tier 1 catalog), `HandleListTools()` (tier 2 tool loading), `BuildServerCatalog()`, `ExecuteWithPlan()`, lazy connections, tool routing |
| `mcp_client.go` | Stdio transport: JSON-RPC over stdin/stdout, `GetServerDescription()` from initialize handshake |
| `mcp_client_http.go` | HTTP/streamable-http transport: JSON-RPC over HTTP POST, `GetServerDescription()` from stored serverInfo, session management via `mcp-session-id` header |
| `mcp_client_interface.go` | Common interface: `Start()`, `Initialize()`, `ListTools()`, `CallTool()`, `GetTools()`, `GetServerDescription()`, `Close()` |
| `mcp_jit.go` | `MCPDiscoverTool` + `MCPListToolsTool` definitions, `IsMCPDiscoverCall()`, `IsMCPListToolsCall()`, `MatchToolPattern()` |
| `mcp_sessions.go` | `MCPSessionManager`: session reuse, 30-min TTL, 5-min cleanup interval |
| `mcp_definitions.go` | Server config types, `MCPServerConfig.Description` field, auto-enable modes |
| `mcp_code_api.go` | Context injection: `InjectJITContext()` prepends system message explaining two-step discovery workflow |
| `mcp_security_config.go` | Blocked commands (79), shell metachar filtering, env var sanitization |
| `mcp_command_resolver.go` | Intelligent command resolution: npx→pnpm→yarn→bunx fallback chain |

### Chat Handler Integration
| File | Purpose |
|------|---------|
| `routes.go` (~93KB) | Main chat handler. MCP integration at lines ~2470-2960: manager creation, `mcp_discover`/`mcp_list_tools` interception, tool execution, multi-round loop |
| `routes_tools.go` | `/api/tools` and `/api/tools/search` endpoints |

### Parsers (`model/parsers/`)
| File | Purpose |
|------|---------|
| `parsers.go` | Parser registry, `ParserForName()` |
| `qwen3coder.go` | Qwen3 tool call format parser |
| Various others | Model-specific parsers (DeepSeek, OLMo3, etc.) |

## API Endpoints

### POST /api/chat (MCP-enabled)
```json
{
  "model": "qwen3-coder:30b",
  "messages": [...],
  "mcp_servers": [
    {"name": "fs", "description": "File system operations", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"]},
    {"name": "remote", "description": "Remote API tools", "transport": "http", "url": "http://host:8085/mcp", "headers": {...}}
  ],
  "session_id": "persist-across-requests",
  "max_tool_rounds": 15,
  "jit_max_tools": 5
}
```

### GET/POST /api/tools — List server definitions or tools
### POST /api/tools/search — Search tools by glob pattern

## JIT Discovery Flow (routes.go)

1. **Manager creation** (~line 2475): Resolve servers, create/reuse MCPManager, inject `mcp_discover` + `mcp_list_tools` + cached tools
2. **Generation loop**: Model generates tokens, parser detects tool calls
3. **Tier 1 interception** (~line 2779): `mcp_discover` → `HandleDiscovery(pattern)` → server catalog summary (no tool injection)
4. **Tier 2 interception** (~line 2810): `mcp_list_tools` → `HandleListTools(serverNames)` → inject tool schemas → continue
5. **Tool execution** (~line 2900): `AnalyzeExecutionPlan()` → `ExecuteWithPlan()` (parallel when possible) → inject results → continue
6. **Termination**: Model produces text-only response (no tool calls)

## MCPManager Key Methods

```go
// Tier 1: Server catalog
BuildServerCatalog(pattern string) []ServerCatalogEntry
HandleDiscovery(pattern string) (string, error)  // catalog summary only

// Tier 2: Tool loading
HandleListTools(serverNames []string) ([]api.Tool, string, error)  // tool schemas + summary

// Active tool set
GetActiveTools() []api.Tool  // returns MCPDiscoverTool + MCPListToolsTool + discoveredTools

// Description resolution (config → handshake → fallback)
resolveServerDescription(serverName string) string
```

## MCP Server Configuration Loading Priority
1. `~/.ollama/mcp-servers.json`
2. `/etc/ollama/mcp-servers.json`
3. `./mcp-servers.json`
4. `OLLAMA_MCP_SERVERS` env var

## Session Persistence
- Sessions keyed by `session_id` from `ChatRequest`
- Discovered tools persist across requests within same session
- Config changes trigger session recreation
- 30-minute TTL, in-memory only

## Security Layers
- 79 blocked commands (shells, sudo, curl, rm, etc.)
- 11 blocked shell metacharacters
- 15+ filtered environment variables (AWS keys, tokens, etc.)
- Path traversal prevention
- Process group isolation for stdio clients

## Known Issues
- **mcp_discover text leak**: Streaming parser sometimes fails to intercept `mcp_discover[ARGS]{...}` syntax, passing it through as content text. Affects both manager and worker models in the hestia-OS stack. Root cause is in the parser/routes.go token detection.
- **Context canceled errors**: Normal on client disconnect or session conflict.

## Building & Running
```bash
cd /home/velvetm/Desktop/ollama
go build .
./ollama serve   # Listens on :11434
```

## Integration with Hestia-OS
The orchestrator at `:8000` sends `mcp_servers` (with `description` fields) pointing to A2A bridge endpoints (`:8001`, `:8002`, `:8003`). Ollama discovers each bridge's capabilities via two-tier MCP flow and executes tools. The bridges handle async task execution and webhook callbacks.

### Downstream (agentic_flow) Integration Points
- `_FILTERED_TOOLS`: `{"mcp_discover", "mcp_list_tools", "respond_to_manager"}` — suppresses progress events
- `_SYSTEM_TOOLS`: `frozenset({"mcp_discover", "mcp_list_tools", "respond_to_manager"})` — extraction pre-gate
- `_WORKER_MCP_RULES`: Two-step discovery instructions for worker models
- `agent_client.py:get_mcp_servers()`: Includes `description` from agent card in server configs
- `main.py`: Ontology MCP server registered with description

### Prompt Layering
When MCP servers are configured, Ollama's `InjectJITContext()` prepends its own system context (tool call format, two-step discovery instructions) to the message stream. This is the **Ollama layer** of a multi-layer prompt stack. The agentic_flow Python code adds its own layers *before* Ollama sees the request:
- Agent identity + date context (from `build_worker_prompt()` or `build_system_prompt()`)
- Ollama then prepends JIT context on top

The worker MCP prompt in agentic_flow intentionally avoids duplicating JIT instructions (tool call format, discovery syntax) since Ollama handles that layer.

## GPUs
RTX PRO 6000 (x2, 98GB each) + RTX 5090 (33GB). Use `CUDA_VISIBLE_DEVICES=2,1,0`.
