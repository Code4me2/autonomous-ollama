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

func TestJITState_Basic(t *testing.T) {
	state := NewJITState(5)

	if state == nil {
		t.Fatal("NewJITState returned nil")
	}

	if state.MaxToolsPerDiscovery != 5 {
		t.Errorf("MaxToolsPerDiscovery = %d, want 5", state.MaxToolsPerDiscovery)
	}

	if state.GetDiscoveredToolCount() != 0 {
		t.Errorf("GetDiscoveredToolCount() = %d, want 0", state.GetDiscoveredToolCount())
	}

	if state.GetPendingServerCount() != 0 {
		t.Errorf("GetPendingServerCount() = %d, want 0", state.GetPendingServerCount())
	}
}

func TestJITState_DefaultMaxTools(t *testing.T) {
	// Test that 0 or negative values default to 5
	state := NewJITState(0)
	if state.MaxToolsPerDiscovery != 5 {
		t.Errorf("NewJITState(0).MaxToolsPerDiscovery = %d, want 5", state.MaxToolsPerDiscovery)
	}

	state = NewJITState(-1)
	if state.MaxToolsPerDiscovery != 5 {
		t.Errorf("NewJITState(-1).MaxToolsPerDiscovery = %d, want 5", state.MaxToolsPerDiscovery)
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
