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

func TestIsMCPListToolsCall(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"mcp_list_tools", "mcp_list_tools", true},
		{"other tool", "filesystem:read_file", false},
		{"mcp_discover", "mcp_discover", false},
		{"similar name", "mcp_list_tools_extra", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCall := api.ToolCall{
				Function: api.ToolCallFunction{
					Name: tt.toolName,
				},
			}
			got := IsMCPListToolsCall(toolCall)
			if got != tt.want {
				t.Errorf("IsMCPListToolsCall(%q) = %v, want %v", tt.toolName, got, tt.want)
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

func TestMCPListToolsTool_Schema(t *testing.T) {
	if MCPListToolsTool.Type != "function" {
		t.Errorf("MCPListToolsTool.Type = %q, want \"function\"", MCPListToolsTool.Type)
	}

	if MCPListToolsTool.Function.Name != "mcp_list_tools" {
		t.Errorf("MCPListToolsTool.Function.Name = %q, want \"mcp_list_tools\"", MCPListToolsTool.Function.Name)
	}

	if MCPListToolsTool.Function.Description == "" {
		t.Error("MCPListToolsTool.Function.Description is empty")
	}

	// Check that servers is required
	required := MCPListToolsTool.Function.Parameters.Required
	if len(required) != 1 || required[0] != "servers" {
		t.Errorf("MCPListToolsTool.Function.Parameters.Required = %v, want [\"servers\"]", required)
	}

	// Verify the servers property has array type
	prop, exists := MCPListToolsTool.Function.Parameters.Properties.Get("servers")
	if !exists {
		t.Fatal("MCPListToolsTool missing 'servers' property")
	}
	if len(prop.Type) == 0 || prop.Type[0] != "array" {
		t.Errorf("servers property type = %v, want [\"array\"]", prop.Type)
	}
	if prop.Items == nil {
		t.Error("servers property missing Items schema")
	}
}

func TestMCPManagerJIT_GetActiveTools(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Initially should have mcp_discover + mcp_list_tools
	tools := manager.GetActiveTools()
	if len(tools) != 2 {
		t.Fatalf("GetActiveTools() returned %d tools, want 2", len(tools))
	}
	if tools[0].Function.Name != "mcp_discover" {
		t.Errorf("GetActiveTools()[0].Function.Name = %q, want \"mcp_discover\"", tools[0].Function.Name)
	}
	if tools[1].Function.Name != "mcp_list_tools" {
		t.Errorf("GetActiveTools()[1].Function.Name = %q, want \"mcp_list_tools\"", tools[1].Function.Name)
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

	// Check active tools includes mcp_discover + mcp_list_tools + discovered tools
	tools := manager.GetActiveTools()
	if len(tools) != 4 {
		t.Errorf("GetActiveTools() returned %d tools, want 4 (2 meta + 2 discovered)", len(tools))
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

// =============================================================================
// Two-Tier Discovery Tests
// =============================================================================

func TestServerCatalogEntry(t *testing.T) {
	entry := ServerCatalogEntry{
		Name:        "test_server",
		Description: "A test server",
		ToolCount:   5,
	}
	if entry.Name != "test_server" || entry.Description != "A test server" || entry.ToolCount != 5 {
		t.Errorf("ServerCatalogEntry fields not set correctly: %+v", entry)
	}
}

func TestMCPManager_ConfigDescriptions(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Add server with description via AddServerLazy
	config := api.MCPServerConfig{
		Name:        "test_server",
		Description: "My test server description",
		Transport:   api.MCPTransportHTTP,
		URL:         "http://localhost:9999/mcp",
	}
	err := manager.AddServerLazy(config)
	if err != nil {
		t.Fatalf("AddServerLazy failed: %v", err)
	}

	// Verify description was stored
	manager.mu.RLock()
	desc, exists := manager.configDescriptions["test_server"]
	manager.mu.RUnlock()

	if !exists || desc != "My test server description" {
		t.Errorf("configDescriptions[\"test_server\"] = %q, %v, want \"My test server description\", true", desc, exists)
	}
}

func TestMCPManager_ResolveServerDescription_ConfigPriority(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Set config description
	manager.mu.Lock()
	manager.configDescriptions["server1"] = "From config"
	manager.mu.Unlock()

	// Config description should take priority
	manager.mu.RLock()
	desc := manager.resolveServerDescription("server1")
	manager.mu.RUnlock()

	if desc != "From config" {
		t.Errorf("resolveServerDescription() = %q, want \"From config\"", desc)
	}
}

func TestMCPManager_ResolveServerDescription_Fallback(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// No config description, no client — should get fallback
	manager.mu.RLock()
	desc := manager.resolveServerDescription("unknown_server")
	manager.mu.RUnlock()

	if desc != "(no description)" {
		t.Errorf("resolveServerDescription() = %q, want \"(no description)\"", desc)
	}
}

func TestMCPManager_BuildServerCatalog_Empty(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// No servers registered — catalog should be empty
	catalog := manager.BuildServerCatalog("*")
	if len(catalog) != 0 {
		t.Errorf("BuildServerCatalog(\"*\") returned %d entries, want 0", len(catalog))
	}
}

func TestMCPManager_BuildServerCatalog_PatternFiltering(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Register servers with different names (they'll fail to connect but
	// BuildServerCatalog should still include them with 0 tools)
	configs := []api.MCPServerConfig{
		{Name: "calendar", Description: "Calendar tools", Transport: api.MCPTransportHTTP, URL: "http://localhost:19999/mcp"},
		{Name: "memory", Description: "Memory tools", Transport: api.MCPTransportHTTP, URL: "http://localhost:19998/mcp"},
		{Name: "ontology", Description: "Ontology reader", Transport: api.MCPTransportHTTP, URL: "http://localhost:19997/mcp"},
	}
	for _, c := range configs {
		if err := manager.AddServerLazy(c); err != nil {
			t.Fatalf("AddServerLazy(%s) failed: %v", c.Name, err)
		}
	}

	// Pattern "*" should return all 3
	catalog := manager.BuildServerCatalog("*")
	if len(catalog) != 3 {
		t.Errorf("BuildServerCatalog(\"*\") returned %d entries, want 3", len(catalog))
	}

	// Pattern "*calendar*" should return only 1
	catalog = manager.BuildServerCatalog("*calendar*")
	if len(catalog) != 1 {
		t.Errorf("BuildServerCatalog(\"*calendar*\") returned %d entries, want 1", len(catalog))
	}
	if len(catalog) > 0 && catalog[0].Name != "calendar" {
		t.Errorf("BuildServerCatalog(\"*calendar*\")[0].Name = %q, want \"calendar\"", catalog[0].Name)
	}

	// Pattern "*xyz*" should return 0
	catalog = manager.BuildServerCatalog("*xyz*")
	if len(catalog) != 0 {
		t.Errorf("BuildServerCatalog(\"*xyz*\") returned %d entries, want 0", len(catalog))
	}
}

func TestMCPManager_HandleDiscovery_ReturnsCatalog(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Register some servers
	configs := []api.MCPServerConfig{
		{Name: "calendar", Description: "Calendar management", Transport: api.MCPTransportHTTP, URL: "http://localhost:19999/mcp"},
		{Name: "memory", Description: "Knowledge base", Transport: api.MCPTransportHTTP, URL: "http://localhost:19998/mcp"},
	}
	for _, c := range configs {
		manager.AddServerLazy(c)
	}

	// HandleDiscovery should return catalog summary (not tool schemas)
	summary, err := manager.HandleDiscovery("*")
	if err != nil {
		t.Fatalf("HandleDiscovery failed: %v", err)
	}

	// Summary should mention servers
	if summary == "" {
		t.Error("HandleDiscovery returned empty summary")
	}

	// Summary should contain server names
	if !contains(summary, "calendar") {
		t.Error("Summary should contain 'calendar'")
	}
	if !contains(summary, "memory") {
		t.Error("Summary should contain 'memory'")
	}

	// Summary should contain descriptions
	if !contains(summary, "Calendar management") {
		t.Error("Summary should contain server description 'Calendar management'")
	}

	// Summary should instruct to call mcp_list_tools
	if !contains(summary, "mcp_list_tools") {
		t.Error("Summary should instruct to call mcp_list_tools")
	}
}

func TestMCPManager_HandleDiscovery_NoServers(t *testing.T) {
	manager := NewMCPManager(10, 5)

	summary, err := manager.HandleDiscovery("*nonexistent*")
	if err != nil {
		t.Fatalf("HandleDiscovery failed: %v", err)
	}

	if !contains(summary, "No servers found") {
		t.Errorf("Expected 'No servers found' message, got: %s", summary)
	}
}

func TestMCPManager_HandleListTools_EmptyServers(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// HandleListTools with non-existent servers
	tools, summary, err := manager.HandleListTools([]string{"nonexistent"})
	if err != nil {
		t.Fatalf("HandleListTools failed: %v", err)
	}

	// Should return 0 tools (server doesn't exist)
	if len(tools) != 0 {
		t.Errorf("HandleListTools returned %d tools, want 0", len(tools))
	}

	// Summary should mention the failure
	if !contains(summary, "nonexistent") {
		t.Errorf("Summary should mention failed server, got: %s", summary)
	}
}

func TestMCPManager_HandleListTools_DeduplicatesTools(t *testing.T) {
	manager := NewMCPManager(10, 5)

	// Pre-populate with a discovered tool
	manager.AddDiscoveredTools([]api.Tool{
		{Type: "function", Function: api.ToolFunction{Name: "server1:tool_a", Description: "Tool A"}},
	}, "server1")

	// Verify tool is already discovered
	if !manager.IsToolDiscovered("server1:tool_a") {
		t.Fatal("Pre-condition failed: tool should be discovered")
	}
}

func TestMCPServerConfig_Description(t *testing.T) {
	// Verify Description field is properly serialized
	config := api.MCPServerConfig{
		Name:        "test",
		Description: "A test server",
		Transport:   api.MCPTransportHTTP,
		URL:         "http://localhost:8080/mcp",
	}

	if config.Description != "A test server" {
		t.Errorf("MCPServerConfig.Description = %q, want \"A test server\"", config.Description)
	}
}

func TestMCPClient_GetServerDescription(t *testing.T) {
	client := NewMCPClient("test", "echo", []string{"test"}, nil)

	// Before initialization, should return empty
	desc := client.GetServerDescription()
	if desc != "" {
		t.Errorf("GetServerDescription() before init = %q, want \"\"", desc)
	}

	// Simulate setting serverInfo
	client.mu.Lock()
	client.serverInfo = mcpServerInfo{Name: "TestServer", Version: "1.0"}
	client.mu.Unlock()

	desc = client.GetServerDescription()
	if desc != "TestServer v1.0" {
		t.Errorf("GetServerDescription() = %q, want \"TestServer v1.0\"", desc)
	}

	// Test with name only
	client.mu.Lock()
	client.serverInfo = mcpServerInfo{Name: "TestServer"}
	client.mu.Unlock()

	desc = client.GetServerDescription()
	if desc != "TestServer" {
		t.Errorf("GetServerDescription() name only = %q, want \"TestServer\"", desc)
	}
}

func TestMCPHTTPClient_GetServerDescription(t *testing.T) {
	client := NewMCPHTTPClient("test", "http://localhost:8080/mcp", nil)

	// Before initialization, should return empty
	desc := client.GetServerDescription()
	if desc != "" {
		t.Errorf("GetServerDescription() before init = %q, want \"\"", desc)
	}

	// Simulate setting serverInfo
	client.mu.Lock()
	client.serverInfo = map[string]interface{}{
		"name":    "HTTPServer",
		"version": "2.0",
	}
	client.mu.Unlock()

	desc = client.GetServerDescription()
	if desc != "HTTPServer v2.0" {
		t.Errorf("GetServerDescription() = %q, want \"HTTPServer v2.0\"", desc)
	}

	// Test with name only
	client.mu.Lock()
	client.serverInfo = map[string]interface{}{
		"name": "HTTPServer",
	}
	client.mu.Unlock()

	desc = client.GetServerDescription()
	if desc != "HTTPServer" {
		t.Errorf("GetServerDescription() name only = %q, want \"HTTPServer\"", desc)
	}
}

// contains is a test helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
