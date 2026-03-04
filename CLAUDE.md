# Ollama MCP Fork - Architecture Guide

## Overview

This is a custom fork of Ollama with native MCP (Model Context Protocol) support. It enables autonomous multi-pass tool execution where inference pauses on tool calls, tools are executed via MCP servers, and generation resumes with results—all within a single API request.

## Key Modifications

The fork adds MCP support through several new files in `server/`:

| File | Purpose |
|------|---------|
| `mcp.go` | Core MCP integration, `MCPManager` |
| `mcp_client.go` | MCP client for stdio transport (spawned processes) |
| `mcp_client_http.go` | MCP client for HTTP transport (remote servers) |
| `mcp_client_interface.go` | Common interface for MCP clients |
| `mcp_definitions.go` | MCP protocol types (JSON-RPC, tools, etc.) |
| `mcp_jit.go` | JIT tool discovery via `mcp_discover` built-in |
| `mcp_command_resolver.go` | Resolves MCP server commands |
| `mcp_code_api.go` | Code generation API helpers |
| `routes.go` | Modified to handle MCP in chat flow (line ~2700+) |

## MCP Flow

### Standard Tool Calling (without MCP)
```
Client → POST /api/chat with tools
Model generates tool_call
← Returns tool_call to client
Client executes tool
Client → POST /api/chat with tool result
Model generates response
← Returns response
```

### MCP-Enhanced Flow (this fork) — Two-Tier Discovery
```
Client → POST /api/chat with mcp_servers config
Ollama registers MCP servers lazily
Model calls mcp_discover (tier 1: server catalog)
← Ollama returns server names, descriptions, tool counts
Model calls mcp_list_tools (tier 2: load tools from servers)
← Ollama connects to servers, returns tool schemas
Model calls actual tool (e.g., generate_code)
← Ollama intercepts, executes via MCP, injects result
Model generates final response
← Returns response (all in single request!)
```

## API Extensions

### mcp_servers Parameter
Added to `/api/chat` request body:

```json
{
  "model": "ministral-3:14b",
  "messages": [...],
  "mcp_servers": [
    {
      "name": "OllamaAgent",
      "transport": "http",
      "url": "http://localhost:8001/mcp",
      "query_params": {
        "session_id": "abc123",
        "webhook": "http://localhost:8000/webhook"
      }
    }
  ]
}
```

### Transport Types

#### HTTP Transport (`mcp_client_http.go`)
- Used for remote MCP servers
- Sends JSON-RPC over HTTP POST
- Supports query params (session_id, webhook URL)
- Stateless sessions per request

#### Stdio Transport (`mcp_client.go`)
- Spawns local MCP server process
- Communicates via stdin/stdout
- Used for local tools (filesystem, shell, etc.)

## Built-in Tools

### mcp_discover (Tier 1)
Server catalog discovery — returns available servers with descriptions and tool counts.

```json
{
  "name": "mcp_discover",
  "parameters": { "pattern": "*" }
}
```

Returns server catalog. Model then calls `mcp_list_tools` to load actual tools.

### mcp_list_tools (Tier 2)
Loads tool schemas from specific servers.

```json
{
  "name": "mcp_list_tools",
  "parameters": { "servers": ["ServerName"] }
}
```

Returns tool definitions that become available for subsequent calls.

## Key Code Locations

### Chat Handler (`routes.go`)
The main chat handling is in `ChatHandler` around line 2600+. MCP integration points:

1. **MCP Manager Creation** (~line 2650): Creates `MCPManager` from `mcp_servers` config
2. **Tool Call Interception** (~line 2700): Checks if tool call is MCP tool
3. **MCP Execution** (~line 2720): Calls tool via MCP client
4. **Result Injection** (~line 2750): Injects result into conversation
5. **Continue Generation** (~line 2780): Resumes inference with result

### MCP Manager (`mcp.go`)
```go
type MCPManager struct {
    clients    map[string]MCPClientInterface
    toolIndex  map[string]string  // tool_name → server_name
}

func (m *MCPManager) Initialize() error
func (m *MCPManager) DiscoverTools(pattern string) ([]Tool, error)
func (m *MCPManager) CallTool(name string, args map[string]any) (any, error)
func (m *MCPManager) Close()
```

### HTTP Client (`mcp_client_http.go`)
```go
type MCPHTTPClient struct {
    name        string
    baseURL     string
    queryParams map[string]string
    sessionID   string
    httpClient  *http.Client
}

func (c *MCPHTTPClient) Initialize() error
func (c *MCPHTTPClient) ListTools() ([]Tool, error)
func (c *MCPHTTPClient) CallTool(name, args) (*ToolResult, error)
```

## Known Issues

### New MCP Session Per Request
Each `/api/chat` request creates a fresh MCP session:
1. Sends `initialize` to MCP server
2. Sends `notifications/initialized`
3. Sends `tools/list`

This is inefficient but ensures clean state. Session reuse would require:
- Tracking MCP sessions across Ollama requests
- Handling session expiration/cleanup
- More complex state management

### mcp_discover Called Every Turn
The model often calls `mcp_discover` on each turn because:
1. Tool list is not persisted in conversation context
2. Model doesn't "remember" available tools
3. System prompt doesn't list tools explicitly

Potential fixes:
- Add tool list to system prompt
- Cache tool list in conversation context
- Use static tools instead of JIT discovery

### Context Canceled Errors
Sometimes seen as:
```
level=ERROR source=routes.go:2707 msg="Completion failed" error="context canceled"
```

Causes:
1. Client disconnected mid-generation
2. Second request to same session interrupted first
3. Timeout during long tool execution

## Integration with Orchestrator

The orchestrator at `localhost:8000` uses this Ollama fork:

1. **Orchestrator** sends `/api/chat` with `mcp_servers` pointing to A2A bridge
2. **Ollama** calls `mcp_discover` → bridge returns available tools
3. **Ollama** calls tool (e.g., `OllamaAgent:generate_code`)
4. **Bridge** executes async, returns task_id immediately
5. **Ollama** continues generation (may say "working on it...")
6. **Bridge** POSTs result to webhook when done
7. **Orchestrator** injects result on next generation

## Building

```bash
cd /home/velvetm/Desktop/ollama
go build .
./ollama serve
```

## Debugging

### Enable Debug Logging
```bash
OLLAMA_DEBUG=1 ./ollama serve
```

### Key Log Messages
- `"Starting MCP manager"` - MCP initialization
- `"MCP tool call:"` - Tool being executed
- `"MCP tool result:"` - Result received
- `"Completion failed"` - Generation error

### Test MCP Manually
```bash
curl -X POST http://localhost:11434/api/chat -d '{
  "model": "ministral-3:14b",
  "messages": [{"role": "user", "content": "What tools do you have?"}],
  "mcp_servers": [{
    "name": "test",
    "transport": "http",
    "url": "http://localhost:8001/mcp"
  }]
}'
```

## Related Documentation

- `/home/velvetm/Desktop/ollama_mod.md` - Original implementation plan
- `/home/velvetm/Desktop/hestia-OS/agentic_flow/CLAUDE.md` - Orchestrator architecture
- MCP Spec: https://spec.modelcontextprotocol.io/
