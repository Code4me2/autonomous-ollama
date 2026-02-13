package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ollama/ollama/api"
)

// MCPHTTPClient manages communication with a remote MCP server via streamable-http transport.
// This transport uses a single HTTP endpoint that accepts POST requests and streams responses.
type MCPHTTPClient struct {
	name    string
	url     string // Full URL to MCP endpoint (e.g., http://host:port/mcp)
	headers map[string]string

	// HTTP client with connection pooling
	client *http.Client

	// State
	mu          sync.RWMutex
	initialized bool
	tools       []api.Tool
	serverInfo  map[string]interface{}
	sessionID   string // MCP session ID for streamable-http

	// Request tracking
	requestID int64

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// NewMCPHTTPClient creates a new HTTP-based MCP client for streamable-http transport
func NewMCPHTTPClient(name, url string, headers map[string]string) *MCPHTTPClient {
	ctx, cancel := context.WithCancel(context.Background())

	return &MCPHTTPClient{
		name:    name,
		url:     url,
		headers: headers,
		client: &http.Client{
			Timeout: 0, // No timeout - we handle timeouts per-request
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
				MaxIdleConnsPerHost: 5,
			},
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start initializes the HTTP client (no persistent connection needed)
func (c *MCPHTTPClient) Start() error {
	slog.Info("MCP HTTP client ready", "name", c.name, "url", c.url)
	return nil
}

// Initialize performs MCP protocol initialization
func (c *MCPHTTPClient) Initialize() error {
	c.mu.RLock()
	if c.initialized {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	slog.Debug("Initializing MCP HTTP client", "name", c.name)

	// Send initialize request
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"roots": map[string]bool{
				"listChanged": true,
			},
		},
		"clientInfo": map[string]string{
			"name":    "ollama",
			"version": "1.0.0",
		},
	}

	var initResult struct {
		ProtocolVersion string                 `json:"protocolVersion"`
		Capabilities    map[string]interface{} `json:"capabilities"`
		ServerInfo      map[string]interface{} `json:"serverInfo"`
	}

	// Use callWithSessionCapture to get session ID from response headers
	if err := c.callWithSessionCapture("initialize", initParams, &initResult); err != nil {
		return fmt.Errorf("MCP initialize failed: %w", err)
	}

	c.mu.Lock()
	c.serverInfo = initResult.ServerInfo
	c.initialized = true
	c.mu.Unlock()

	// Send initialized notification
	if err := c.notify("notifications/initialized", nil); err != nil {
		slog.Warn("Failed to send initialized notification", "name", c.name, "error", err)
	}

	slog.Info("MCP HTTP client initialized",
		"name", c.name,
		"server", initResult.ServerInfo,
		"protocol", initResult.ProtocolVersion,
		"session", c.sessionID)

	return nil
}

// ListTools retrieves the list of available tools from the server
func (c *MCPHTTPClient) ListTools() ([]api.Tool, error) {
	var result mcpListToolsResponse
	if err := c.call("tools/list", nil, &result); err != nil {
		return nil, err
	}

	tools := make([]api.Tool, 0, len(result.Tools))
	for _, mcpTool := range result.Tools {
		tool := api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        c.name + ":" + mcpTool.Name,
				Description: mcpTool.Description,
			},
		}

		// Convert input schema to properties (same as WebSocket client)
		if mcpTool.InputSchema != nil {
			props := api.NewToolPropertiesMap()
			if properties, ok := mcpTool.InputSchema["properties"].(map[string]interface{}); ok {
				for propName, propValue := range properties {
					if propMap, ok := propValue.(map[string]interface{}); ok {
						prop := api.ToolProperty{}
						if t, ok := propMap["type"].(string); ok {
							prop.Type = []string{t}
						}
						if d, ok := propMap["description"].(string); ok {
							prop.Description = d
						}
						props.Set(propName, prop)
					}
				}
			}
			tool.Function.Parameters = api.ToolFunctionParameters{
				Type:       "object",
				Properties: props,
			}
			if required, ok := mcpTool.InputSchema["required"].([]interface{}); ok {
				for _, r := range required {
					if rs, ok := r.(string); ok {
						tool.Function.Parameters.Required = append(tool.Function.Parameters.Required, rs)
					}
				}
			}
		}

		tools = append(tools, tool)
	}

	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()

	slog.Info("MCP HTTP tools listed", "name", c.name, "count", len(tools))
	return tools, nil
}

// CallTool invokes a tool on the MCP server
func (c *MCPHTTPClient) CallTool(name string, args map[string]interface{}) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
	defer cancel()

	// Strip the server name prefix from the tool name
	// Tools are registered as "servername:toolname" but MCP server expects just "toolname"
	actualName := name
	prefix := c.name + ":"
	if strings.HasPrefix(name, prefix) {
		actualName = strings.TrimPrefix(name, prefix)
	}

	params := map[string]interface{}{
		"name":      actualName,
		"arguments": args,
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}

	if err := c.callWithContext(ctx, "tools/call", params, &result); err != nil {
		return "", err
	}

	// Extract text content
	var output string
	for _, content := range result.Content {
		if content.Type == "text" {
			output += content.Text
		}
	}

	if result.IsError {
		return output, fmt.Errorf("tool error: %s", output)
	}

	return output, nil
}

// GetTools returns the cached list of tools
func (c *MCPHTTPClient) GetTools() []api.Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools
}

// Close shuts down the HTTP client
func (c *MCPHTTPClient) Close() error {
	slog.Info("Shutting down MCP HTTP client", "name", c.name)
	c.cancel()
	c.client.CloseIdleConnections()
	return nil
}

// call sends a JSON-RPC request and waits for the response
func (c *MCPHTTPClient) call(method string, params interface{}, result interface{}) error {
	return c.callWithContext(c.ctx, method, params, result)
}

// callWithSessionCapture is used for initialize to capture the session ID from response headers
func (c *MCPHTTPClient) callWithSessionCapture(method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&c.requestID, 1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      &id,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(c.ctx, "POST", c.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Capture session ID from response header
	if sessionID := resp.Header.Get("mcp-session-id"); sessionID != "" {
		c.mu.Lock()
		c.sessionID = sessionID
		c.mu.Unlock()
		slog.Debug("Captured MCP session ID", "name", c.name, "session", sessionID)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "text/event-stream" || contentType == "application/x-ndjson" {
		return c.handleStreamingResponse(resp.Body, id, result)
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("failed to unmarshal result: %w", err)
		}
	}

	return nil
}

func (c *MCPHTTPClient) callWithContext(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&c.requestID, 1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      &id,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	slog.Debug("Sending MCP HTTP request", "name", c.name, "method", method, "id", id)

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	// Add session ID if we have one
	c.mu.RLock()
	if c.sessionID != "" {
		httpReq.Header.Set("mcp-session-id", c.sessionID)
	}
	c.mu.RUnlock()

	// Add custom headers
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	// Send request
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}

	// Check if response is streaming (SSE-style) or single JSON
	contentType := resp.Header.Get("Content-Type")

	if contentType == "text/event-stream" || contentType == "application/x-ndjson" {
		// Handle streaming response
		return c.handleStreamingResponse(resp.Body, id, result)
	}

	// Handle single JSON response
	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("failed to unmarshal result: %w", err)
		}
	}

	return nil
}

// handleStreamingResponse processes a streaming HTTP response
func (c *MCPHTTPClient) handleStreamingResponse(body io.Reader, expectedID int64, result interface{}) error {
	scanner := bufio.NewScanner(body)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and SSE prefixes
		if line == "" {
			continue
		}
		if len(line) > 6 && line[:6] == "data: " {
			line = line[6:]
		}
		if line == "" || line == "[DONE]" {
			continue
		}

		var rpcResp jsonRPCResponse
		if err := json.Unmarshal([]byte(line), &rpcResp); err != nil {
			slog.Debug("Skipping non-JSON line", "line", truncateString(line, 50))
			continue
		}

		// Check if this is our response
		if rpcResp.ID != nil && *rpcResp.ID == expectedID {
			if rpcResp.Error != nil {
				return fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
			}

			if result != nil && rpcResp.Result != nil {
				if err := json.Unmarshal(rpcResp.Result, result); err != nil {
					return fmt.Errorf("failed to unmarshal result: %w", err)
				}
			}
			return nil
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stream: %w", err)
	}

	return errors.New("no response received for request")
}

// notify sends a JSON-RPC notification (no response expected)
func (c *MCPHTTPClient) notify(method string, params interface{}) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		// No ID for notifications
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(c.ctx, "POST", c.url, bytes.NewReader(data))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()

	return nil
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
