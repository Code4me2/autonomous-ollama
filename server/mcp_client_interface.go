package server

import "github.com/ollama/ollama/api"

// MCPClientInterface defines the interface for MCP client implementations.
// Supports stdio and streamable-http transports.
type MCPClientInterface interface {
	// Start initiates the connection to the MCP server
	Start() error

	// Initialize performs MCP protocol initialization
	Initialize() error

	// ListTools retrieves the list of available tools from the server
	ListTools() ([]api.Tool, error)

	// CallTool invokes a tool on the MCP server
	CallTool(name string, args map[string]interface{}) (string, error)

	// GetTools returns the cached list of tools
	GetTools() []api.Tool

	// Close shuts down the connection
	Close() error
}

// NewMCPClientFromConfig creates an MCP client based on the server configuration.
// It automatically selects the appropriate transport (stdio or http).
func NewMCPClientFromConfig(config api.MCPServerConfig, opts ...MCPClientOption) MCPClientInterface {
	transport := config.Transport
	if transport == "" {
		transport = api.MCPTransportStdio // Default to stdio
	}

	switch transport {
	case api.MCPTransportHTTP, api.MCPTransportStreamableHTTP:
		return NewMCPHTTPClient(config.Name, config.URL, config.Headers)
	default:
		return NewMCPClient(config.Name, config.Command, config.Args, config.Env, opts...)
	}
}
