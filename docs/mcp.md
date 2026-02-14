# MCP (Model Context Protocol) Integration

Ollama supports the Model Context Protocol (MCP), enabling language models to execute tools and interact with external systems autonomously.

> **Status**: Experimental

## Quick Start

### CLI Usage

```bash
# Run with filesystem tools restricted to a directory
ollama run qwen2.5:7b --tools /path/to/directory

# In a git repository, both filesystem AND git tools auto-enable
ollama run qwen2.5:7b --tools /path/to/git-repo

# Example interaction
>>> List all files in the directory
# Model will automatically execute filesystem:list_directory tool

>>> Show the git status
# Model will automatically execute git:status tool (if in a git repo)
```

### API Usage

```bash
curl -X POST http://localhost:11434/api/chat \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [{"role": "user", "content": "List the files"}],
    "mcp_servers": [{
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/safe/path"]
    }]
  }'
```

## How It Works

1. **Model generates tool call** in JSON format
2. **Parser detects** the tool call during streaming
3. **MCP server executes** the tool via JSON-RPC over stdio
4. **Results returned** to model context
5. **Model continues** generation with tool results
6. **Loop repeats** for multi-step tasks (up to 15 rounds)

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Public API (mcp.go)                          │
│  GetMCPServersForTools()    - Get servers for --tools flag      │
│  GetMCPManager()            - Get/create manager (always JIT)   │
│  ResolveServersForRequest() - Unified server resolution         │
│  ListMCPServers()           - List available server definitions │
└─────────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┴───────────────────┐
          ▼                                       ▼
┌─────────────────────┐                 ┌─────────────────────┐
│   MCPDefinitions    │                 │  MCPSessionManager  │
│  (mcp_definitions)  │                 │   (mcp_sessions)    │
│                     │                 │                     │
│  Static config of   │                 │  Runtime sessions   │
│  available servers  │                 │  with connections   │
└─────────────────────┘                 └─────────────────────┘
                                                  │
                                                  ▼
                                        ┌─────────────────────┐
                                        │     MCPManager      │
                                        │   (mcp_manager)     │
                                        │                     │
                                        │  Multi-client mgmt  │
                                        │  JIT discovery      │
                                        └─────────────────────┘
                                                  │
                                    ┌─────────────┴─────────────┐
                                    ▼                           ▼
                          ┌─────────────────┐         ┌─────────────────┐
                          │    MCPClient    │         │  MCPHTTPClient  │
                          │  (mcp_client)   │         │(mcp_client_http)│
                          │                 │         │                 │
                          │  stdio transport│         │ HTTP transport  │
                          │  (local process)│         │ (remote server) │
                          └─────────────────┘         └─────────────────┘
```

## Auto-Enable Configuration

MCP servers can declare when they should automatically enable with the `--tools` flag.

### Auto-Enable Modes

| Mode | Description |
|------|-------------|
| `never` | Server must be explicitly configured via API (default) |
| `always` | Server enables whenever `--tools` is used |
| `with_path` | Server enables when `--tools` has a path argument |
| `if_match` | Server enables if conditions in `enable_if` match |

### Conditional Enabling (if_match)

The `enable_if` object supports these conditions:

| Condition | Description |
|-----------|-------------|
| `file_exists` | Check if a file/directory exists in the tools path |
| `env_set` | Check if an environment variable is set (non-empty) |

### Example Configuration

Create `~/.ollama/mcp-servers.json`:

```json
{
  "servers": [
    {
      "name": "filesystem",
      "description": "File system operations",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem"],
      "requires_path": true,
      "auto_enable": "with_path"
    },
    {
      "name": "git",
      "description": "Git repository operations",
      "command": "npx",
      "args": ["-y", "@cyanheads/git-mcp-server"],
      "requires_path": true,
      "auto_enable": "if_match",
      "enable_if": {
        "file_exists": ".git"
      }
    },
    {
      "name": "postgres",
      "description": "PostgreSQL database operations",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-postgres"],
      "auto_enable": "if_match",
      "enable_if": {
        "env_set": "POSTGRES_CONNECTION"
      }
    },
    {
      "name": "python",
      "description": "Python code execution",
      "command": "python",
      "args": ["-m", "mcp_server_python"],
      "auto_enable": "never"
    }
  ]
}
```

With this configuration:
- **filesystem** enables for any `--tools /path`
- **git** enables only if `/path/.git` exists
- **postgres** enables only if `POSTGRES_CONNECTION` env var is set
- **python** never auto-enables (must use API explicitly)

## API Reference

### Chat Endpoint with MCP

**Endpoint:** `POST /api/chat`

**Request:**
```json
{
  "model": "qwen2.5:7b",
  "messages": [{"role": "user", "content": "Your prompt"}],
  "mcp_servers": [
    {
      "name": "server-name",
      "command": "executable",
      "args": ["arg1", "arg2"],
      "env": {"KEY": "value"}
    }
  ],
  "stream": false,
  "max_tool_rounds": 10,
  "tool_timeout": 30000,
  "include_tool_results": true
}
```

**Request Parameters:**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `model` | string | required | Model to use for generation |
| `messages` | []Message | required | Conversation history |
| `mcp_servers` | []MCPServer | - | MCP servers to enable for tool execution |
| `stream` | bool | true | Stream responses (set `false` for single response with tool loop) |
| `max_tool_rounds` | int | 15 | Maximum tool execution rounds before stopping |
| `tool_timeout` | int | 30000 | Timeout per tool execution in milliseconds |
| `include_tool_results` | bool | false | Include raw tool output in response |
| `jit_tools` | bool | true | Enable JIT tool discovery (see below) |
| `jit_max_tools` | int | 5 | Max tools injected per discovery call |
| `jit_connect_eager` | bool | false | Pre-connect servers for faster discovery |

**MCP Server Configuration:**

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique identifier for the server |
| `transport` | string | Transport type: `"stdio"` (default), `"http"`, or `"streamable-http"` |
| `command` | string | Executable to run (stdio transport) |
| `args` | []string | Command-line arguments (stdio transport) |
| `env` | map | Environment variables (stdio transport) |
| `url` | string | HTTP URL for remote server (http/streamable-http transport) |
| `headers` | map | HTTP headers for remote connection |

### Response Format

**Non-streaming response** (`stream: false`):
```json
{
  "model": "qwen2.5:7b",
  "created_at": "2024-01-15T10:30:00Z",
  "message": {
    "role": "assistant",
    "content": "Here are the files in the directory...",
    "tool_calls": [
      {
        "function": {
          "name": "filesystem:list_directory",
          "arguments": {"path": "/home/user"}
        }
      }
    ]
  },
  "tool_results": [
    {
      "tool_name": "filesystem:list_directory",
      "arguments": {"path": "/home/user"},
      "content": "[DIR] Documents\n[DIR] Downloads\n[FILE] readme.txt"
    }
  ],
  "done": true,
  "done_reason": "stop",
  "task_id": "550e8400-e29b-41d4-a716-446655440000",
  "task_status": "completed"
}
```

> **Note:** `tool_results` is only included when `include_tool_results: true` in the request.

**A2A Task Tracking Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `task_id` | string | Unique task identifier (auto-generated if not provided in request) |
| `task_status` | string | Task state: `"working"` during execution, `"completed"` when done |

These fields enable A2A (Agent-to-Agent) protocol compatibility. You can provide a `task_id` in the request to track specific tasks.

### Complete Example

```bash
# Multi-step task with tool results included
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [
      {"role": "user", "content": "List the files in /tmp and read any .txt files you find"}
    ],
    "mcp_servers": [{
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }],
    "stream": false,
    "include_tool_results": true,
    "max_tool_rounds": 5
  }'
```

The model will autonomously:
1. Call `filesystem:list_directory` to list files
2. Identify `.txt` files from the results
3. Call `filesystem:read_file` for each text file
4. Return a synthesized response with all findings

### Server Definition Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique server identifier |
| `description` | string | Human-readable description |
| `command` | string | Executable to run (npx, python, etc.) |
| `args` | []string | Command-line arguments |
| `env` | map | Environment variables |
| `requires_path` | bool | Whether server needs a path argument |
| `path_arg_index` | int | Where to insert path in args (-1 = append) |
| `capabilities` | []string | List of capability tags |
| `auto_enable` | string | Auto-enable mode (never/always/with_path/if_match) |
| `enable_if` | object | Conditions for if_match mode |

## JIT Tool Discovery (Default)

Ollama uses Just-In-Time (JIT) tool discovery by default for API requests. Instead of loading all tools upfront (which can consume significant context), the model starts with only an `mcp_discover` meta-tool and finds tools as needed.

### Why JIT?

| Aspect | Eager Loading | JIT Discovery |
|--------|--------------|---------------|
| Initial context | 500-5000 tokens (all tools) | ~50 tokens (mcp_discover only) |
| Startup time | Slow (connect all servers) | Fast (no connections until needed) |
| Scalability | Limited by context window | Unlimited registered tools |
| Model guidance | Must know all tools upfront | Discovers what it needs |

### How It Works

1. **Request arrives** with `mcp_servers` config
2. **Model receives** only the `mcp_discover` tool
3. **Model decides** it needs file tools, calls `mcp_discover("*file*")`
4. **Ollama connects** to relevant MCP server (lazy connection)
5. **Matching tools injected** (e.g., `filesystem:read_file`, `filesystem:write_file`)
6. **Model uses** the discovered tools
7. **Loop continues** - model can discover more tools if needed

### Discovery Patterns

The `mcp_discover` tool accepts glob patterns:

| Pattern | Matches |
|---------|---------|
| `*file*` | `filesystem:read_file`, `filesystem:write_file`, ... |
| `*git*` | `git:status`, `git:commit`, `git:diff`, ... |
| `*sql*` | `postgres:query`, `mysql:execute`, ... |
| `*search*` | `filesystem:search_files`, `github:search`, ... |
| `*` | All available tools (use sparingly) |

### API Example

JIT is enabled by default. The model automatically discovers tools:

```bash
curl -X POST http://localhost:11434/api/chat \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [{"role": "user", "content": "Read the config file and fix the bug"}],
    "mcp_servers": [{
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/project"]
    }],
    "stream": false
  }'
```

The model will:
1. Receive `mcp_discover` tool
2. Call `mcp_discover("*file*")` or `mcp_discover("*read*")`
3. Get `filesystem:read_file` injected
4. Read the config file
5. Continue working...

### Disabling JIT (Legacy Mode)

To load all tools upfront (not recommended for large tool sets):

```json
{
  "jit_tools": false,
  "mcp_servers": [...]
}
```

### Programmatic Tool Search

Search for tools without a chat request using the `/api/tools/search` endpoint:

```bash
curl -X POST http://localhost:11434/api/tools/search \
  -d '{
    "pattern": "*file*",
    "mcp_servers": [{
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }]
  }'
```

**Response:**
```json
{
  "tools": [
    {
      "server": "filesystem",
      "name": "filesystem:read_file",
      "description": "Read the contents of a file",
      "parameters": {...}
    },
    ...
  ],
  "pattern": "*file*",
  "total": 5
}
```

## Security

### Implemented Safeguards

- **Process isolation**: MCP servers run in separate process groups
- **Path restrictions**: Filesystem access limited to specified directories
- **Environment filtering**: Allowlist-based, sensitive variables removed
- **Command validation**: Dangerous commands (shells, sudo, rm) blocked
- **Argument sanitization**: Shell injection prevention
- **Timeouts**: 30-second default with graceful shutdown

### Blocked Commands

Shells (`bash`, `sh`, `zsh`), privilege escalation (`sudo`, `su`), destructive commands (`rm`, `dd`), and network tools (`curl`, `wget`, `nc`) are blocked by default.

## Creating MCP Servers

MCP servers communicate via JSON-RPC 2.0 over stdin/stdout and must implement three methods:

### Required Methods

1. **`initialize`** - Returns server capabilities
2. **`tools/list`** - Returns available tools with schemas
3. **`tools/call`** - Executes a tool and returns results

### Minimal Python Example

```python
#!/usr/bin/env python3
import json
import sys

def handle_request(request):
    method = request.get("method")
    request_id = request.get("id")

    if method == "initialize":
        return {
            "jsonrpc": "2.0", "id": request_id,
            "result": {
                "protocolVersion": "0.1.0",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "my-server", "version": "1.0.0"}
            }
        }

    elif method == "tools/list":
        return {
            "jsonrpc": "2.0", "id": request_id,
            "result": {
                "tools": [{
                    "name": "hello",
                    "description": "Say hello",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "name": {"type": "string", "description": "Name to greet"}
                        },
                        "required": ["name"]
                    }
                }]
            }
        }

    elif method == "tools/call":
        name = request["params"]["arguments"].get("name", "World")
        return {
            "jsonrpc": "2.0", "id": request_id,
            "result": {
                "content": [{"type": "text", "text": f"Hello, {name}!"}]
            }
        }

if __name__ == "__main__":
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        request = json.loads(line)
        response = handle_request(request)
        sys.stdout.write(json.dumps(response) + "\n")
        sys.stdout.flush()
```

### Testing Your Server

```bash
# Test initialize
echo '{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}' | python3 my_server.py

# Test tools/list
echo '{"jsonrpc":"2.0","method":"tools/list","params":{},"id":2}' | python3 my_server.py

# Test tools/call
echo '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"hello","arguments":{"name":"Alice"}},"id":3}' | python3 my_server.py
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OLLAMA_DEBUG=INFO` | Enable debug logging |
| `OLLAMA_MCP_TIMEOUT` | Tool execution timeout (ms) |
| `OLLAMA_MCP_SERVERS` | JSON config for MCP servers (overrides file) |
| `OLLAMA_MCP_DISABLE=1` | Disable MCP validation on startup |

## Supported Models

MCP works best with models that support tool calling:
- Qwen 2.5 / Qwen 3 series
- Llama 3.1+ with tool support
- Other models with JSON tool call output

## HTTP Transport (Remote MCP Servers)

MCP servers can be accessed remotely via HTTP transport (streamable-http), enabling tools on remote machines (e.g., over Tailscale).

### Configuration

```json
{
  "mcp_servers": [
    {
      "name": "remote-server",
      "transport": "http",
      "url": "http://server.tailnet.ts.net:8085/mcp",
      "headers": {
        "Authorization": "Bearer your-token"
      }
    }
  ]
}
```

The `transport` field accepts:
- `"http"` - HTTP POST with JSON-RPC
- `"streamable-http"` - Alias for http (MCP spec terminology)

### Mixed Local and Remote

```json
{
  "mcp_servers": [
    {
      "name": "local-fs",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home"]
    },
    {
      "name": "remote-calendar",
      "transport": "http",
      "url": "http://calendar-server.tailnet.ts.net:8085/mcp"
    }
  ]
}
```

### How HTTP Transport Works

1. Ollama sends JSON-RPC requests via HTTP POST to the MCP endpoint
2. Server responds with JSON-RPC results
3. Session IDs are tracked via `mcp-session-id` header
4. Tool names are automatically prefixed with server name (e.g., `calendar:list_tasks`)

### Example: Remote Calendar

```bash
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "ministral-3:14b",
    "messages": [{"role": "user", "content": "List my tasks"}],
    "mcp_servers": [{
      "name": "calendar",
      "transport": "http",
      "url": "http://100.119.170.128:8085/mcp"
    }],
    "stream": false
  }'
```

### Tailscale Integration

HTTP over Tailscale provides:
- WireGuard encryption at network layer
- No additional TLS required within tailnet
- Low latency within network

## A2A Bridge (Optional)

For advanced multi-agent orchestration, you can use the **A2A Bridge** - a separate Python application that wraps Ollama with the A2A (Agent-to-Agent) protocol.

### What A2A Bridge Adds

| Feature | Native Ollama | With A2A Bridge |
|---------|---------------|-----------------|
| Tool execution | Synchronous | Sync + Async |
| Task tracking | Basic (`task_id`) | Full lifecycle |
| Agent discovery | None | `/.well-known/agent.json` |
| Push notifications | None | HMAC-signed webhooks |
| Multi-agent delegation | None | Manager → Worker pattern |

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    User / Application                        │
└─────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┴───────────────┐
              ▼                               ▼
┌───────────────────────┐         ┌───────────────────────────┐
│   Direct Ollama API   │         │      A2A Bridge           │
│                       │         │                           │
│  POST /api/chat       │         │  POST /a2a (JSON-RPC)     │
│  - Sync tool calls    │         │  POST /mcp (tool calls)   │
│  - JIT discovery      │         │  GET /.well-known/agent   │
│  - Native MCP         │         │  POST /notifications/*    │
│                       │         │                           │
│  Use for: CLI tools,  │         │  Use for: Multi-agent,    │
│  simple automation    │         │  async tasks, webhooks    │
└───────────────────────┘         └───────────────────────────┘
              │                               │
              └───────────────┬───────────────┘
                              ▼
                    ┌─────────────────┐
                    │     Ollama      │
                    │   (MCP Engine)  │
                    └─────────────────┘
```

### When to Use Each

**Use Native Ollama (`ollama run --tools` or `/api/chat`):**
- CLI interactions
- Simple tool execution
- Single-task automation
- Direct API access

**Use A2A Bridge:**
- Multi-agent coordination (Manager → Workers)
- Async task delegation with callbacks
- Agent discovery and skill-based routing
- Push notifications on task completion
- Building agent networks

### A2A Bridge Repository

The A2A Bridge is maintained separately:
- Repository: [github.com/Code4me2/agentic_flow](https://github.com/Code4me2/agentic_flow)
- Features: Async delegation, push notifications, turn-boundary injection

## OpenAI Compatibility Endpoint

MCP is also available via the OpenAI-compatible endpoint:

```bash
curl -X POST http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [{"role": "user", "content": "List the files"}],
    "mcp_servers": [{
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }],
    "tools_path": "/tmp",
    "jit_max_tools": 10
  }'
```

## Limitations

- **Protocol**: MCP 1.0
- **Concurrency**: Max 10 parallel MCP servers
- **Platform**: Linux/macOS (Windows untested)
- **HTTP timeout**: 60 seconds per tool call (configurable per-server planned)

## Troubleshooting

**"Tool not found"**
- Verify MCP server initialized correctly
- Check tool name includes namespace prefix (e.g., `filesystem:read_file`)
- For remote servers, ensure URL is reachable

**"MCP server failed to initialize"**
- Check command path exists
- Verify Python/Node environment
- Test server manually with JSON input

**"No MCP servers matched for --tools context"**
- Check auto_enable settings in config
- Verify path exists and conditions match

**"Access denied"**
- Path outside allowed directories
- Security policy violation

**"Connection refused" (HTTP transport)**
- Verify remote server is running
- Check URL includes correct port and path
- Ensure firewall/network allows connection

**Debug mode:**
```bash
OLLAMA_DEBUG=INFO ollama serve
```

## Resources

- [MCP Specification](https://modelcontextprotocol.io/docs)
- [Official MCP Servers](https://github.com/modelcontextprotocol/servers)
- [A2A Bridge (Multi-Agent)](https://github.com/Code4me2/agentic_flow)
