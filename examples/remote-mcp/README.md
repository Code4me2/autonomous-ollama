# Remote MCP Server Setup via Tailscale

This guide explains how to expose MCP (Model Context Protocol) servers over a network using WebSocket transport, enabling Ollama to use tools on remote machines.

## Architecture

```
┌─────────────────────────────┐         Tailscale Network         ┌─────────────────────────────┐
│  Your Machine (Ollama)      │◄─────────────────────────────────►│  Remote Machine (MCP)       │
│                             │      WireGuard Encrypted          │                             │
│  IP: 100.64.x.x             │                                   │  IP: 100.64.y.y             │
│  Hostname: laptop           │                                   │  Hostname: server           │
│                             │                                   │                             │
│  ┌───────────────────────┐  │         WebSocket                 │  ┌───────────────────────┐  │
│  │      Ollama API       │  │◄────────────────────────────────►│  │   MCP WS Bridge       │  │
│  │   localhost:11434     │  │    ws://server:8080              │  │      :8080            │  │
│  └───────────────────────┘  │                                   │  └───────────┬───────────┘  │
│                             │                                   │              │ stdio        │
└─────────────────────────────┘                                   │  ┌───────────▼───────────┐  │
                                                                  │  │   MCP Server          │  │
                                                                  │  │ (filesystem, git, etc)│  │
                                                                  │  └───────────────────────┘  │
                                                                  └─────────────────────────────┘
```

## Prerequisites

- [Tailscale](https://tailscale.com/) installed on both machines
- Node.js 18+ on the remote machine
- Ollama with WebSocket transport support on your machine

---

## Part 1: Server Side Setup (Remote Machine)

### Step 1: Verify Tailscale

```bash
# Check Tailscale status
tailscale status

# Note your Tailscale IP and hostname
tailscale ip -4
# Example: 100.64.32.15

# Get your full hostname
tailscale status --self
# Example: server.tailnet-name.ts.net
```

### Step 2: Set Up the Bridge

```bash
# Create directory
mkdir -p ~/mcp-bridge
cd ~/mcp-bridge

# Initialize and install dependencies
npm init -y
npm install ws

# Copy bridge.js from this example directory
cp /path/to/ollama/examples/remote-mcp/bridge.js .
```

### Step 3: Configure the MCP Server

Edit environment variables or modify `bridge.js` directly:

**For filesystem access:**
```bash
export MCP_COMMAND="npx"
export MCP_ARGS='["-y", "@modelcontextprotocol/server-filesystem", "/home/user/data"]'
```

**For git operations:**
```bash
export MCP_COMMAND="npx"
export MCP_ARGS='["-y", "@cyanheads/git-mcp-server", "--repository", "/path/to/repo"]'
```

**For a custom Python MCP server:**
```bash
export MCP_COMMAND="python3"
export MCP_ARGS='["/path/to/your/mcp_server.py"]'
```

### Step 4: Run the Bridge

**Without authentication:**
```bash
node bridge.js
```

**With authentication:**
```bash
MCP_AUTH_TOKEN=my-secret-token node bridge.js
```

**Custom port:**
```bash
MCP_PORT=9000 node bridge.js
```

### Step 5: Verify It's Running

```bash
# Health check
curl http://localhost:8080/health

# Test WebSocket (install wscat: npm install -g wscat)
wscat -c ws://localhost:8080

# Send MCP initialize request
{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"clientInfo":{"name":"test","version":"1.0.0"}},"id":1}
```

---

## Part 2: Run as a System Service

### Step 1: Install the Service

```bash
# Copy and edit the service file
sudo cp /path/to/ollama/examples/remote-mcp/mcp-bridge.service /etc/systemd/system/

# Edit to replace YOUR_USERNAME and configure paths
sudo nano /etc/systemd/system/mcp-bridge.service
```

### Step 2: Enable and Start

```bash
sudo systemctl daemon-reload
sudo systemctl enable mcp-bridge
sudo systemctl start mcp-bridge

# Check status
sudo systemctl status mcp-bridge

# View logs
journalctl -u mcp-bridge -f
```

### Step 3: Manage the Service

```bash
# Stop
sudo systemctl stop mcp-bridge

# Restart
sudo systemctl restart mcp-bridge

# Disable
sudo systemctl disable mcp-bridge
```

---

## Part 3: Client Side (Ollama Machine)

### Step 1: Find Remote Machine Address

```bash
# List all machines on your tailnet
tailscale status

# Test connectivity
ping server.tailnet-name.ts.net

# Verify bridge is accessible
curl http://server.tailnet-name.ts.net:8080/health
```

### Step 2: API Calls

**Basic request (no auth):**
```bash
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [
      {"role": "user", "content": "List all files in the data directory"}
    ],
    "mcp_servers": [
      {
        "name": "remote-filesystem",
        "transport": "websocket",
        "url": "ws://server.tailnet-name.ts.net:8080"
      }
    ],
    "stream": false
  }'
```

**With authentication:**
```bash
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [
      {"role": "user", "content": "List all files in the data directory"}
    ],
    "mcp_servers": [
      {
        "name": "remote-filesystem",
        "transport": "websocket",
        "url": "ws://server.tailnet-name.ts.net:8080",
        "headers": {
          "Authorization": "Bearer my-secret-token"
        }
      }
    ],
    "stream": false
  }'
```

**Using Tailscale IP directly:**
```bash
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [
      {"role": "user", "content": "Read the config.json file"}
    ],
    "mcp_servers": [
      {
        "name": "remote-fs",
        "transport": "websocket",
        "url": "ws://100.64.32.15:8080"
      }
    ],
    "stream": true
  }'
```

---

## Part 4: Multiple Remote Servers

Connect to multiple MCP servers simultaneously:

```bash
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5:7b",
    "messages": [
      {"role": "user", "content": "Check git status on the build server and list files on the storage server"}
    ],
    "mcp_servers": [
      {
        "name": "build-server-git",
        "transport": "websocket",
        "url": "ws://build.tailnet.ts.net:8080"
      },
      {
        "name": "storage-server-fs",
        "transport": "websocket",
        "url": "ws://storage.tailnet.ts.net:8080",
        "headers": {
          "Authorization": "Bearer storage-token"
        }
      },
      {
        "name": "local-tools",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-filesystem", "/local/path"]
      }
    ],
    "stream": false
  }'
```

---

## Part 5: A2A Bridge Integration

If using the A2A bridge (`a2a_ollama_bridge.py`), configure remote servers:

```python
# Environment-based configuration
import os

REMOTE_MCP_URL = os.getenv("REMOTE_MCP_URL", "ws://server.tailnet.ts.net:8080")
REMOTE_MCP_TOKEN = os.getenv("REMOTE_MCP_TOKEN", "")

# In your request building
mcp_servers = [
    {
        "name": "remote-tools",
        "transport": "websocket",
        "url": REMOTE_MCP_URL,
        "headers": {"Authorization": f"Bearer {REMOTE_MCP_TOKEN}"} if REMOTE_MCP_TOKEN else {}
    }
]

ollama_request = {
    "model": model,
    "messages": messages,
    "mcp_servers": mcp_servers,
    "stream": True
}
```

---

## Part 6: Security Considerations

### Authentication

Always use authentication for production:

```bash
# Generate a secure token
openssl rand -hex 32

# Set on server
export MCP_AUTH_TOKEN=<generated-token>

# Use in client requests
"headers": {"Authorization": "Bearer <generated-token>"}
```

### Tailscale ACLs

Restrict access using Tailscale ACLs:

```json
{
  "acls": [
    {
      "action": "accept",
      "src": ["tag:ollama-clients"],
      "dst": ["tag:mcp-servers:8080"]
    }
  ],
  "tagOwners": {
    "tag:ollama-clients": ["your-email@example.com"],
    "tag:mcp-servers": ["your-email@example.com"]
  }
}
```

### Filesystem Restrictions

Limit MCP server access to specific directories:

```bash
# Only expose /home/user/data, not entire home
MCP_ARGS='["-y", "@modelcontextprotocol/server-filesystem", "/home/user/data"]'
```

---

## Troubleshooting

| Issue | Cause | Solution |
|-------|-------|----------|
| Connection refused | Bridge not running | `systemctl status mcp-bridge` |
| Tailscale timeout | Firewall blocking | Check `tailscale status`, verify port 8080 open |
| Auth failures | Token mismatch | Verify `MCP_AUTH_TOKEN` matches header |
| MCP server crash | Bad command/args | Check `journalctl -u mcp-bridge -f` |
| Tool timeout | Network latency or slow tool | Increase timeout in Ollama config |
| "Tool not found" | JIT discovery issue | Use `mcp_discover` with correct pattern |

### Debug Mode

**On the bridge (server):**
```bash
# Logs show all WS<->MCP traffic
journalctl -u mcp-bridge -f
```

**On Ollama (client):**
```bash
OLLAMA_DEBUG=1 ollama serve
```

### Test MCP Protocol Manually

```bash
# Connect to bridge
wscat -c ws://server.tailnet.ts.net:8080

# Initialize
{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"clientInfo":{"name":"test","version":"1.0.0"}},"id":1}

# List tools
{"jsonrpc":"2.0","method":"tools/list","params":{},"id":2}

# Call a tool
{"jsonrpc":"2.0","method":"tools/call","params":{"name":"list_directory","arguments":{"path":"/"}},"id":3}
```

---

## Files in This Directory

| File | Description |
|------|-------------|
| `bridge.js` | WebSocket bridge that wraps stdio MCP servers |
| `mcp-bridge.service` | Systemd service file for running bridge as daemon |
| `README.md` | This documentation |

---

## Related Documentation

- [MCP Documentation](../docs/mcp.md) - Full MCP integration docs
- [MCP Issues Tracker](../docs/mcp-jit-issues.md) - Known issues and status
- [MCP Specification](https://modelcontextprotocol.io/docs) - Official protocol spec
