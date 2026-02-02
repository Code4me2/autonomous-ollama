// Package server provides MCP (Model Context Protocol) integration for Ollama.
//
// MCP Architecture:
//
//	┌─────────────────────────────────────────────────────────────────┐
//	│                    Public API (this file)                       │
//	│  GetMCPServersForTools()    - Get servers for --tools flag      │
//	│  GetMCPManager()            - Get/create manager with JIT       │
//	│  ResolveServersForRequest() - Unified server resolution         │
//	│  ListMCPServers()           - List available server definitions │
//	└─────────────────────────────────────────────────────────────────┘
//	                              │
//	          ┌───────────────────┴───────────────────┐
//	          ▼                                       ▼
//	┌─────────────────────┐                 ┌─────────────────────┐
//	│   MCPDefinitions    │                 │  MCPSessionManager  │
//	│  (mcp_definitions)  │                 │   (mcp_sessions)    │
//	│                     │                 │                     │
//	│  Static config of   │                 │  Runtime sessions   │
//	│  available servers  │                 │  with connections   │
//	└─────────────────────┘                 └─────────────────────┘
//	                                                  │
//	                                                  ▼
//	                                        ┌─────────────────────┐
//	                                        │     MCPManager      │
//	                                        │   (mcp_manager)     │
//	                                        │                     │
//	                                        │  Multi-client mgmt  │
//	                                        │  Tool execution     │
//	                                        └─────────────────────┘
//	                                                  │
//	                                                  ▼
//	                                        ┌─────────────────────┐
//	                                        │      MCPClient      │
//	                                        │    (mcp_client)     │
//	                                        │                     │
//	                                        │  Single JSON-RPC    │
//	                                        │  connection         │
//	                                        └─────────────────────┘

package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
)

// ============================================================================
// Public API - Clean interface for external code
// ============================================================================

// GetMCPServersForTools returns the MCP server configs that should be enabled
// for the given tools spec. It handles path normalization:
//   - "." or "true" → current working directory
//   - "~/path" → expands to home directory
//   - relative paths → resolved to absolute paths
//
// Returns the server configs and the resolved absolute path.
// On error, still returns the resolved path so callers can implement fallback.
// This is used by the --tools CLI flag.
func GetMCPServersForTools(toolsSpec string) ([]api.MCPServerConfig, string, error) {
	// Normalize the tools path first (needed even for fallback on error)
	toolsPath := toolsSpec
	if toolsSpec == "." || toolsSpec == "true" {
		if cwd, err := os.Getwd(); err == nil {
			toolsPath = cwd
		}
	}

	// Expand tilde to home directory
	if strings.HasPrefix(toolsPath, "~") {
		if home := os.Getenv("HOME"); home != "" {
			toolsPath = filepath.Join(home, toolsPath[1:])
		}
	}

	// Resolve to absolute path
	if absPath, err := filepath.Abs(toolsPath); err == nil {
		toolsPath = absPath
	}

	// Load definitions
	defs, err := LoadMCPDefinitions()
	if err != nil {
		return nil, toolsPath, err
	}

	ctx := AutoEnableContext{ToolsPath: toolsPath}
	return defs.GetAutoEnableServers(ctx), toolsPath, nil
}

// GetMCPManager returns an MCP manager for the given session and configs.
// All managers use JIT discovery - servers are registered but not connected until needed.
// If a session with matching configs already exists, it will be reused.
func GetMCPManager(sessionID string, configs []api.MCPServerConfig, maxToolsPerDiscovery int) (*MCPManager, error) {
	return GetMCPSessionManager().GetOrCreateManager(sessionID, configs, maxToolsPerDiscovery)
}

// ListMCPServers returns information about all available MCP server definitions.
func ListMCPServers() ([]MCPServerInfo, error) {
	defs, err := LoadMCPDefinitions()
	if err != nil {
		return nil, err
	}
	return defs.ListServers(), nil
}

// ResolveServersForRequest returns the unified set of MCP servers for a request.
// It merges explicit servers (req.MCPServers) with auto-enabled servers from tools path.
// Explicit servers take precedence over auto-enabled servers with the same name.
func ResolveServersForRequest(req api.ChatRequest) ([]api.MCPServerConfig, error) {
	servers := make([]api.MCPServerConfig, 0, len(req.MCPServers))
	serverNames := make(map[string]bool)

	// Explicit servers take precedence
	for _, s := range req.MCPServers {
		servers = append(servers, s)
		serverNames[s.Name] = true
	}

	// Add auto-enabled servers if tools path provided
	if req.ToolsPath != "" {
		defs, err := LoadMCPDefinitions()
		if err != nil {
			// Graceful degradation - return explicit servers only
			return servers, nil
		}
		ctx := AutoEnableContext{ToolsPath: req.ToolsPath}
		for _, autoServer := range defs.GetAutoEnableServers(ctx) {
			if !serverNames[autoServer.Name] {
				servers = append(servers, autoServer)
				serverNames[autoServer.Name] = true
			}
		}
	}

	return servers, nil
}
