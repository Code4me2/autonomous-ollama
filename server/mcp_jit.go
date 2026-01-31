package server

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
)

// JITState tracks discovered tools during a conversation
type JITState struct {
	mu sync.RWMutex

	// DiscoveredTools maps tool name -> tool schema
	// Persists across rounds within a single request
	DiscoveredTools map[string]api.Tool

	// PendingServers holds server configs not yet connected
	PendingServers map[string]api.MCPServerConfig

	// ConnectedServers tracks which servers are actually connected
	ConnectedServers map[string]bool

	// ToolServerMap maps tool name -> server name for routing
	ToolServerMap map[string]string

	// AllToolsCache caches all tools from connected servers
	// Used for pattern matching without re-listing
	AllToolsCache map[string][]api.Tool

	// MaxToolsPerDiscovery limits injection per call
	MaxToolsPerDiscovery int
}

// NewJITState creates a new JIT state tracker
func NewJITState(maxTools int) *JITState {
	if maxTools <= 0 {
		maxTools = 5 // Default
	}
	return &JITState{
		DiscoveredTools:      make(map[string]api.Tool),
		PendingServers:       make(map[string]api.MCPServerConfig),
		ConnectedServers:     make(map[string]bool),
		ToolServerMap:        make(map[string]string),
		AllToolsCache:        make(map[string][]api.Tool),
		MaxToolsPerDiscovery: maxTools,
	}
}

// AddPendingServer registers a server for lazy connection
func (j *JITState) AddPendingServer(config api.MCPServerConfig) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.PendingServers[config.Name] = config
}

// GetActiveTools returns mcp_discover + all discovered tools
func (j *JITState) GetActiveTools() []api.Tool {
	j.mu.RLock()
	defer j.mu.RUnlock()

	tools := []api.Tool{MCPDiscoverTool}
	for _, tool := range j.DiscoveredTools {
		tools = append(tools, tool)
	}
	return tools
}

// AddDiscoveredTools adds newly discovered tools to active set
func (j *JITState) AddDiscoveredTools(tools []api.Tool, serverName string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, tool := range tools {
		j.DiscoveredTools[tool.Function.Name] = tool
		j.ToolServerMap[tool.Function.Name] = serverName
	}
}

// IsToolDiscovered checks if a tool is already available
func (j *JITState) IsToolDiscovered(toolName string) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	_, exists := j.DiscoveredTools[toolName]
	return exists
}

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

// SearchTools searches all pending/connected servers for matching tools
func (j *JITState) SearchTools(
	pattern string,
	manager *MCPManager,
) ([]api.Tool, []string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	var matchedTools []api.Tool
	var serversTried []string
	seen := make(map[string]bool)

	// Search each pending server
	for serverName, config := range j.PendingServers {
		serversTried = append(serversTried, serverName)

		// Connect to server if not already connected
		if !j.ConnectedServers[serverName] {
			if err := manager.AddServer(config); err != nil {
				// Log but continue - server might be unavailable
				slog.Warn("JIT: Failed to connect to MCP server for discovery",
					"server", serverName, "error", err)
				continue
			}
			j.ConnectedServers[serverName] = true
		}

		// Get tools from cache or fetch
		var tools []api.Tool
		if cached, exists := j.AllToolsCache[serverName]; exists {
			tools = cached
		} else {
			var err error
			tools, err = manager.GetToolsFromServer(serverName)
			if err != nil {
				slog.Warn("JIT: Failed to list tools from server",
					"server", serverName, "error", err)
				continue
			}
			j.AllToolsCache[serverName] = tools
		}

		// Match against pattern
		for _, tool := range tools {
			if seen[tool.Function.Name] {
				continue
			}
			if MatchToolPattern(pattern, tool.Function.Name) {
				matchedTools = append(matchedTools, tool)
				seen[tool.Function.Name] = true
				j.ToolServerMap[tool.Function.Name] = serverName

				// Respect limit
				if len(matchedTools) >= j.MaxToolsPerDiscovery {
					return matchedTools, serversTried, nil
				}
			}
		}
	}

	return matchedTools, serversTried, nil
}

// HandleDiscovery processes an mcp_discover call and returns:
// - tools: schemas to inject for next round
// - summary: human-readable result for model context
// - error: any error encountered
func (j *JITState) HandleDiscovery(
	pattern string,
	manager *MCPManager,
) ([]api.Tool, string, error) {
	tools, servers, err := j.SearchTools(pattern, manager)
	if err != nil {
		return nil, "", err
	}

	if len(tools) == 0 {
		return nil, fmt.Sprintf(
			"No tools found matching pattern '%s'. Searched servers: %s. "+
				"Try a different pattern like '*file*', '*git*', or '*' to see all.",
			pattern, strings.Join(servers, ", ")), nil
	}

	// Filter out already discovered tools
	var newTools []api.Tool
	j.mu.RLock()
	for _, tool := range tools {
		if _, exists := j.DiscoveredTools[tool.Function.Name]; !exists {
			newTools = append(newTools, tool)
		}
	}
	j.mu.RUnlock()

	// Build summary for model
	var summaryParts []string
	for _, tool := range tools {
		// Truncate description for summary
		desc := tool.Function.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		summaryParts = append(summaryParts,
			fmt.Sprintf("- %s: %s", tool.Function.Name, desc))
	}

	alreadyKnown := len(tools) - len(newTools)
	summary := fmt.Sprintf(
		"Found %d tools matching '%s':\n%s",
		len(tools), pattern, strings.Join(summaryParts, "\n"))

	if alreadyKnown > 0 {
		summary += fmt.Sprintf("\n\n(%d tools were already available)", alreadyKnown)
	}

	if len(newTools) > 0 {
		summary += "\n\nThese tools are now available. Call them directly in your next response."
	}

	// Add new tools to discovered set
	j.mu.Lock()
	for _, tool := range newTools {
		j.DiscoveredTools[tool.Function.Name] = tool
	}
	j.mu.Unlock()

	slog.Info("JIT: Discovery completed",
		"pattern", pattern,
		"found", len(tools),
		"new", len(newTools),
		"already_known", alreadyKnown)

	return newTools, summary, nil
}

// GetDiscoveredToolCount returns the number of discovered tools
func (j *JITState) GetDiscoveredToolCount() int {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return len(j.DiscoveredTools)
}

// GetPendingServerCount returns the number of pending servers
func (j *JITState) GetPendingServerCount() int {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return len(j.PendingServers)
}
