/**
 * MCP WebSocket Bridge
 *
 * Exposes a stdio-based MCP server over WebSocket for remote access.
 * Designed for use over Tailscale or other secure networks.
 *
 * Usage:
 *   node bridge.js
 *   MCP_AUTH_TOKEN=secret node bridge.js
 *
 * Environment variables:
 *   MCP_AUTH_TOKEN  - Optional auth token for connections
 *   MCP_PORT        - Port to listen on (default: 8080)
 *   MCP_COMMAND     - MCP server command (default: npx)
 *   MCP_ARGS        - MCP server arguments as JSON array
 */

const WebSocket = require('ws');
const { spawn } = require('child_process');
const http = require('http');

// Configuration from environment or defaults
const PORT = parseInt(process.env.MCP_PORT || '8080', 10);
const AUTH_TOKEN = process.env.MCP_AUTH_TOKEN || null;
const MCP_COMMAND = process.env.MCP_COMMAND || 'npx';
const MCP_ARGS = process.env.MCP_ARGS
  ? JSON.parse(process.env.MCP_ARGS)
  : ['-y', '@modelcontextprotocol/server-filesystem', process.env.HOME];

// Track active connections
let connectionCount = 0;

// Create HTTP server for health checks
const httpServer = http.createServer((req, res) => {
  if (req.url === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({
      status: 'healthy',
      connections: wss.clients.size,
      uptime: process.uptime(),
      mcp_command: MCP_COMMAND,
      mcp_args: MCP_ARGS
    }));
  } else if (req.url === '/') {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('MCP WebSocket Bridge\n\nConnect via WebSocket to use MCP tools.\n');
  } else {
    res.writeHead(404);
    res.end('Not found');
  }
});

// Create WebSocket server
const wss = new WebSocket.Server({ server: httpServer });

function log(level, connectionId, message) {
  const timestamp = new Date().toISOString();
  console.log(`[${timestamp}] [${level}] [conn:${connectionId}] ${message}`);
}

wss.on('connection', (ws, req) => {
  const connectionId = ++connectionCount;
  const clientIP = req.headers['x-forwarded-for'] || req.socket.remoteAddress;

  log('INFO', connectionId, `New connection from ${clientIP}`);

  // Validate auth token if configured
  if (AUTH_TOKEN) {
    const authHeader = req.headers['authorization'];
    if (authHeader !== `Bearer ${AUTH_TOKEN}`) {
      log('WARN', connectionId, 'Unauthorized connection attempt');
      ws.close(4001, 'Unauthorized');
      return;
    }
    log('INFO', connectionId, 'Authentication successful');
  }

  // Spawn MCP server process
  const mcp = spawn(MCP_COMMAND, MCP_ARGS, {
    stdio: ['pipe', 'pipe', 'pipe'],
    env: { ...process.env }
  });

  log('INFO', connectionId, `Spawned MCP server (PID: ${mcp.pid}): ${MCP_COMMAND} ${MCP_ARGS.join(' ')}`);

  // Buffer for incomplete JSON lines from MCP
  let buffer = '';

  // WebSocket message -> MCP stdin
  ws.on('message', (data) => {
    const message = data.toString().trim();
    if (message) {
      log('DEBUG', connectionId, `WS->MCP: ${message.substring(0, 200)}${message.length > 200 ? '...' : ''}`);
      try {
        // Validate it's valid JSON before sending
        JSON.parse(message);
        mcp.stdin.write(message + '\n');
      } catch (e) {
        log('ERROR', connectionId, `Invalid JSON from client: ${e.message}`);
      }
    }
  });

  // MCP stdout -> WebSocket
  mcp.stdout.on('data', (data) => {
    buffer += data.toString();

    // Process complete lines (MCP uses newline-delimited JSON)
    const lines = buffer.split('\n');
    buffer = lines.pop(); // Keep incomplete line in buffer

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed) {
        log('DEBUG', connectionId, `MCP->WS: ${trimmed.substring(0, 200)}${trimmed.length > 200 ? '...' : ''}`);
        try {
          // Validate it's valid JSON before sending
          JSON.parse(trimmed);
          ws.send(trimmed);
        } catch (e) {
          log('ERROR', connectionId, `Invalid JSON from MCP: ${e.message}`);
        }
      }
    }
  });

  // Log MCP stderr
  mcp.stderr.on('data', (data) => {
    const text = data.toString().trim();
    if (text) {
      log('WARN', connectionId, `MCP stderr: ${text}`);
    }
  });

  // Handle MCP process exit
  mcp.on('close', (code, signal) => {
    log('INFO', connectionId, `MCP server exited (code: ${code}, signal: ${signal})`);
    if (ws.readyState === WebSocket.OPEN) {
      ws.close(1000, 'MCP server closed');
    }
  });

  mcp.on('error', (err) => {
    log('ERROR', connectionId, `MCP process error: ${err.message}`);
    if (ws.readyState === WebSocket.OPEN) {
      ws.close(1011, 'MCP server error');
    }
  });

  // Handle WebSocket close
  ws.on('close', (code, reason) => {
    log('INFO', connectionId, `WebSocket closed (code: ${code}, reason: ${reason})`);
    if (mcp.exitCode === null) {
      log('INFO', connectionId, 'Killing MCP server');
      mcp.kill('SIGTERM');
      // Force kill after 5 seconds if still running
      setTimeout(() => {
        if (mcp.exitCode === null) {
          log('WARN', connectionId, 'Force killing MCP server');
          mcp.kill('SIGKILL');
        }
      }, 5000);
    }
  });

  ws.on('error', (err) => {
    log('ERROR', connectionId, `WebSocket error: ${err.message}`);
    if (mcp.exitCode === null) {
      mcp.kill('SIGTERM');
    }
  });

  // Send initial connection acknowledgment
  log('INFO', connectionId, 'Connection established, ready for MCP protocol');
});

// Handle server shutdown gracefully
process.on('SIGTERM', () => {
  console.log('Received SIGTERM, shutting down...');
  wss.clients.forEach(ws => ws.close(1001, 'Server shutting down'));
  httpServer.close(() => {
    console.log('Server closed');
    process.exit(0);
  });
});

process.on('SIGINT', () => {
  console.log('Received SIGINT, shutting down...');
  wss.clients.forEach(ws => ws.close(1001, 'Server shutting down'));
  httpServer.close(() => {
    console.log('Server closed');
    process.exit(0);
  });
});

// Start server
httpServer.listen(PORT, '0.0.0.0', () => {
  console.log('='.repeat(60));
  console.log('MCP WebSocket Bridge');
  console.log('='.repeat(60));
  console.log(`WebSocket:    ws://0.0.0.0:${PORT}`);
  console.log(`Health check: http://0.0.0.0:${PORT}/health`);
  console.log(`MCP command:  ${MCP_COMMAND} ${MCP_ARGS.join(' ')}`);
  console.log(`Auth:         ${AUTH_TOKEN ? 'ENABLED' : 'DISABLED'}`);
  console.log('='.repeat(60));
});
