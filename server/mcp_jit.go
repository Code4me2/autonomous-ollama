package server

import (
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
)

// MCPDiscoverTool is the built-in meta-tool for discovering available MCP servers
var MCPDiscoverTool = api.Tool{
	Type: "function",
	Function: api.ToolFunction{
		Name: "mcp_discover",
		Description: `Discover available MCP servers and their capabilities.

WHEN TO USE: Call this FIRST to see what servers are available.
Returns a catalog of servers with name, description, and tool count.

After discovering servers, use mcp_list_tools to load tools from specific servers.

PATTERNS:
- "*" - List all available servers
- "*calendar*" - Servers matching pattern

RETURNS: Server catalog. Then call mcp_list_tools with the server names you need.`,
		Parameters: api.ToolFunctionParameters{
			Type:     "object",
			Required: []string{"pattern"},
			Properties: func() *api.ToolPropertiesMap {
				m := api.NewToolPropertiesMap()
				m.Set("pattern", api.ToolProperty{
					Type:        []string{"string"},
					Description: "Glob pattern to match server names (e.g., '*', '*calendar*')",
				})
				return m
			}(),
		},
	},
}

// MCPListToolsTool is the built-in meta-tool for loading tools from specific servers
var MCPListToolsTool = api.Tool{
	Type: "function",
	Function: api.ToolFunction{
		Name: "mcp_list_tools",
		Description: `Load tools from specific MCP servers.

WHEN TO USE: After calling mcp_discover, call this with the server names you need.
The tools become available for your next action.

RETURNS: Tool definitions from the requested servers.`,
		Parameters: api.ToolFunctionParameters{
			Type:     "object",
			Required: []string{"servers"},
			Properties: func() *api.ToolPropertiesMap {
				m := api.NewToolPropertiesMap()
				m.Set("servers", api.ToolProperty{
					Type:        []string{"array"},
					Description: "List of server names to load tools from",
					Items:       map[string]interface{}{"type": "string"},
				})
				return m
			}(),
		},
	},
}

// IsMCPDiscoverCall checks if a tool call is for mcp_discover
func IsMCPDiscoverCall(toolCall api.ToolCall) bool {
	return toolCall.Function.Name == "mcp_discover"
}

// IsMCPListToolsCall checks if a tool call is for mcp_list_tools
func IsMCPListToolsCall(toolCall api.ToolCall) bool {
	return toolCall.Function.Name == "mcp_list_tools"
}

// MatchToolPattern checks if a tool name matches a glob pattern
// Supports: * (any chars), ? (single char)
func MatchToolPattern(pattern, toolName string) bool {
	// Handle common patterns efficiently
	pattern = strings.ToLower(pattern)
	toolName = strings.ToLower(toolName)

	// Exact match
	if pattern == toolName {
		return true
	}

	// "*" matches everything
	if pattern == "*" {
		return true
	}

	// For patterns like "*file*", use simple substring matching
	// This is more intuitive than strict glob semantics
	trimmed := strings.Trim(pattern, "*")
	if trimmed != "" && trimmed != pattern {
		// Pattern had wildcards - check for substring
		if strings.Contains(toolName, trimmed) {
			return true
		}
	}

	// Fall back to filepath.Match for complex patterns like "file?" or "file[0-9]"
	// But we need to handle the case where pattern doesn't have wildcards at edges
	matched, err := filepath.Match(pattern, toolName)
	if err == nil && matched {
		return true
	}

	// Try with wildcards added if the pattern doesn't already have them
	if !strings.HasPrefix(pattern, "*") && !strings.HasSuffix(pattern, "*") {
		// Try as substring
		if strings.Contains(toolName, pattern) {
			return true
		}
	}

	return false
}
