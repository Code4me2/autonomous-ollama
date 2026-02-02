package server

import (
	"testing"

	"github.com/ollama/ollama/api"
)

func TestMatchToolPattern(t *testing.T) {
	tests := []struct {
		pattern  string
		toolName string
		want     bool
	}{
		// Wildcard patterns
		{"*file*", "filesystem:read_file", true},
		{"*file*", "filesystem:write_file", true},
		{"*file*", "filesystem:list_directory", true}, // "file" matches in "filesystem"
		{"*", "anything", true},
		{"*", "filesystem:read_file", true},

		// Exact match
		{"git:status", "git:status", true},
		{"git:status", "git:commit", false},

		// Prefix patterns
		{"filesystem:*", "filesystem:read_file", true},
		{"filesystem:*", "git:status", false},

		// Suffix patterns
		{"*:status", "git:status", true},
		{"*:status", "git:commit", false},

		// Case insensitivity
		{"*FILE*", "filesystem:read_file", true},
		{"*Git*", "git:status", true},
		{"GIT:STATUS", "git:status", true},

		// Substring without wildcards (should also match)
		{"file", "filesystem:read_file", true},
		{"git", "git:status", true},

		// Common tool discovery patterns
		{"*read*", "filesystem:read_file", true},
		{"*write*", "filesystem:write_file", true},
		{"*list*", "filesystem:list_directory", true},
		{"*directory*", "filesystem:list_directory", true},
		{"*search*", "filesystem:search_files", true},

		// Database patterns
		{"*sql*", "postgres:query_sql", true},
		{"*postgres*", "postgres:execute", true},
		{"*database*", "database:connect", true},

		// No match
		{"*xyz*", "filesystem:read_file", false},
		{"git:*", "filesystem:read_file", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.toolName, func(t *testing.T) {
			got := MatchToolPattern(tt.pattern, tt.toolName)
			if got != tt.want {
				t.Errorf("MatchToolPattern(%q, %q) = %v, want %v",
					tt.pattern, tt.toolName, got, tt.want)
			}
		})
	}
}

func TestMCPManagerJIT_Basic(t *testing.T) {
	manager := NewMCPManager(10, 5)

	if manager == nil {
		t.Fatal("NewMCPManager returned nil")
	}

	if manager.GetMaxToolsPerDiscovery() != 5 {
		t.Errorf("GetMaxToolsPerDiscovery() = %d, want 5", manager.GetMaxToolsPerDiscovery())
	}

	if manager.GetDiscoveredToolCount() != 0 {
		t.Errorf("GetDiscoveredToolCount() = %d, want 0", manager.GetDiscoveredToolCount())
	}

	if manager.GetPendingServerCount() != 0 {
		t.Errorf("GetPendingServerCount() = %d, want 0", manager.GetPendingServerCount())
	}
}

func TestMCPManagerJIT_DefaultMaxTools(t *testing.T) {
	// Test that 0 or negative values default to 5
	manager := NewMCPManager(10, 0)
	if manager.GetMaxToolsPerDiscovery() != 5 {
		t.Errorf("NewMCPManager(10, 0).GetMaxToolsPerDiscovery() = %d, want 5", manager.GetMaxToolsPerDiscovery())
	}

	manager = NewMCPManager(10, -1)
	if manager.GetMaxToolsPerDiscovery() != 5 {
		t.Errorf("NewMCPManager(10, -1).GetMaxToolsPerDiscovery() = %d, want 5", manager.GetMaxToolsPerDiscovery())
	}
}

func TestIsMCPDiscoverCall(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"mcp_discover", "mcp_discover", true},
		{"other tool", "filesystem:read_file", false},
		{"similar name", "mcp_discover_tools", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCall := api.ToolCall{
				Function: api.ToolCallFunction{
					Name: tt.toolName,
				},
			}
			got := IsMCPDiscoverCall(toolCall)
			if got != tt.want {
				t.Errorf("IsMCPDiscoverCall(%q) = %v, want %v", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestMCPDiscoverTool_Schema(t *testing.T) {
	// Verify the mcp_discover tool has expected structure
	if MCPDiscoverTool.Type != "function" {
		t.Errorf("MCPDiscoverTool.Type = %q, want \"function\"", MCPDiscoverTool.Type)
	}

	if MCPDiscoverTool.Function.Name != "mcp_discover" {
		t.Errorf("MCPDiscoverTool.Function.Name = %q, want \"mcp_discover\"", MCPDiscoverTool.Function.Name)
	}

	if MCPDiscoverTool.Function.Description == "" {
		t.Error("MCPDiscoverTool.Function.Description is empty")
	}

	// Check that pattern is required
	required := MCPDiscoverTool.Function.Parameters.Required
	if len(required) != 1 || required[0] != "pattern" {
		t.Errorf("MCPDiscoverTool.Function.Parameters.Required = %v, want [\"pattern\"]", required)
	}
}

func TestMCPManagerJIT_GetActiveTools(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Initially should only have mcp_discover
	tools := manager.GetActiveTools()
	if len(tools) != 1 {
		t.Errorf("GetActiveTools() returned %d tools, want 1", len(tools))
	}
	if tools[0].Function.Name != "mcp_discover" {
		t.Errorf("GetActiveTools()[0].Function.Name = %q, want \"mcp_discover\"", tools[0].Function.Name)
	}
}

func TestMCPManagerJIT_AddDiscoveredTools(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Add some discovered tools
	testTools := []api.Tool{
		{Type: "function", Function: api.ToolFunction{Name: "test_tool_1"}},
		{Type: "function", Function: api.ToolFunction{Name: "test_tool_2"}},
	}
	manager.AddDiscoveredTools(testTools, "test_server")

	// Check count
	if manager.GetDiscoveredToolCount() != 2 {
		t.Errorf("GetDiscoveredToolCount() = %d, want 2", manager.GetDiscoveredToolCount())
	}

	// Check active tools includes mcp_discover + discovered tools
	tools := manager.GetActiveTools()
	if len(tools) != 3 {
		t.Errorf("GetActiveTools() returned %d tools, want 3", len(tools))
	}

	// Verify tool routing was set
	client, exists := manager.GetToolClient("test_tool_1")
	if !exists || client != "test_server" {
		t.Errorf("GetToolClient(\"test_tool_1\") = %q, %v, want \"test_server\", true", client, exists)
	}
}

func TestMCPManagerJIT_IsToolDiscovered(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Initially no tools discovered
	if manager.IsToolDiscovered("test_tool") {
		t.Error("IsToolDiscovered(\"test_tool\") should be false initially")
	}

	// Add a discovered tool
	manager.AddDiscoveredTools([]api.Tool{
		{Type: "function", Function: api.ToolFunction{Name: "test_tool"}},
	}, "test_server")

	// Now it should be discovered
	if !manager.IsToolDiscovered("test_tool") {
		t.Error("IsToolDiscovered(\"test_tool\") should be true after adding")
	}
}
