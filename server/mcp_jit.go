package server

import (
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
)

// MCPDiscoverTool is the built-in meta-tool for JIT discovery
var MCPDiscoverTool = api.Tool{
	Type: "function",
	Function: api.ToolFunction{
		Name: "mcp_discover",
		Description: `Search for available tools by capability pattern.

WHEN TO USE: Call this when you need a tool you don't currently have.
After calling, matching tools become available for your next action.

PATTERNS:
- "*file*" or "*read*" - File operations (read, write, list, search)
- "*git*" - Git operations (status, commit, diff, log)
- "*sql*" or "*postgres*" or "*database*" - Database operations
- "*search*" - Search capabilities
- "*http*" or "*fetch*" - HTTP/API operations
- "*" - List all available tools (use sparingly)

RETURNS: Description of discovered tools. Use them in your next response.`,
		Parameters: api.ToolFunctionParameters{
			Type:     "object",
			Required: []string{"pattern"},
			Properties: func() *api.ToolPropertiesMap {
				m := api.NewToolPropertiesMap()
				m.Set("pattern", api.ToolProperty{
					Type:        []string{"string"},
					Description: "Glob pattern to match tool names (e.g., '*file*', '*git*')",
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
