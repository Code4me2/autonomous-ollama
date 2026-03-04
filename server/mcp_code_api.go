package server

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/ollama/ollama/api"
)

// MCPCodeAPI provides context injection for MCP tools
type MCPCodeAPI struct {
	manager *MCPManager
}

// NewMCPCodeAPI creates a new MCP code API
func NewMCPCodeAPI(manager *MCPManager) *MCPCodeAPI {
	return &MCPCodeAPI{
		manager: manager,
	}
}

// GenerateMinimalContext returns essential runtime context for tool usage.
// Tool schemas are already provided via the template's TypeScript rendering,
// so we only need to add runtime-specific info like working directories.
func (m *MCPCodeAPI) GenerateMinimalContext(configs []api.MCPServerConfig) string {
	slog.Debug("GenerateMinimalContext called", "configs_count", len(configs))

	var context strings.Builder

	// Add filesystem working directory if applicable
	for _, config := range configs {
		if workingDir := m.extractFilesystemPath(config); workingDir != "" {
			context.WriteString(fmt.Sprintf(`
Filesystem working directory: %s
All filesystem tool paths must be within this directory.
`, workingDir))
		}
	}

	result := context.String()
	if result != "" {
		slog.Debug("Generated MCP context", "length", len(result))
	}
	return result
}

// GenerateJITContext returns context explaining the two-step discovery workflow for JIT mode.
func (m *MCPCodeAPI) GenerateJITContext(configs []api.MCPServerConfig) string {
	var context strings.Builder

	context.WriteString(`You have access to external tools via MCP (Model Context Protocol).

IMPORTANT: Tools are discovered in two steps:

STEP 1: Discover available servers:
  [TOOL_CALLS]mcp_discover[ARGS]{"pattern": "*"}
  → Returns a catalog of servers with names, descriptions, and tool counts.

STEP 2: Load tools from the servers you need:
  [TOOL_CALLS]mcp_list_tools[ARGS]{"servers": ["ServerName"]}
  → Returns tool definitions that become available for use.

STEP 3: Use the loaded tools:
  [TOOL_CALLS]ServerName:tool_name[ARGS]{"argument": "value"}

ALWAYS start with mcp_discover to see what servers are available, then use mcp_list_tools to load tools from the servers you need.
`)

	// Add filesystem working directory if applicable
	for _, config := range configs {
		if workingDir := m.extractFilesystemPath(config); workingDir != "" {
			context.WriteString(fmt.Sprintf(`
Filesystem working directory: %s
All file paths must be within this directory.
`, workingDir))
		}
	}

	return context.String()
}

// extractFilesystemPath extracts the working directory from filesystem server config
func (m *MCPCodeAPI) extractFilesystemPath(config api.MCPServerConfig) string {
	isFilesystem := strings.Contains(config.Command, "filesystem") ||
		(len(config.Args) > 0 && strings.Contains(strings.Join(config.Args, " "), "filesystem"))

	if isFilesystem && len(config.Args) > 0 {
		// Path is typically the last argument
		return config.Args[len(config.Args)-1]
	}
	return ""
}

// InjectContextIntoMessages adds runtime context to the message stream
func (m *MCPCodeAPI) InjectContextIntoMessages(messages []api.Message, configs []api.MCPServerConfig) []api.Message {
	context := m.GenerateMinimalContext(configs)
	if context == "" {
		return messages
	}

	// Check if there's already a system message
	if len(messages) > 0 && messages[0].Role == "system" {
		// Append to existing system message
		messages[0].Content += context
	} else {
		// Create new system message
		systemMsg := api.Message{
			Role:    "system",
			Content: context,
		}
		messages = append([]api.Message{systemMsg}, messages...)
	}

	return messages
}

// InjectJITContext adds JIT discovery context to the message stream
func (m *MCPCodeAPI) InjectJITContext(messages []api.Message, configs []api.MCPServerConfig) []api.Message {
	context := m.GenerateJITContext(configs)
	if context == "" {
		return messages
	}

	// Check if there's already a system message
	if len(messages) > 0 && messages[0].Role == "system" {
		// Prepend JIT context to existing system message
		messages[0].Content = context + "\n" + messages[0].Content
	} else {
		// Create new system message
		systemMsg := api.Message{
			Role:    "system",
			Content: context,
		}
		messages = append([]api.Message{systemMsg}, messages...)
	}

	return messages
}
