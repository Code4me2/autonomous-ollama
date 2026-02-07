package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ollama/ollama/api"
)

// MCPWebSocketClient manages communication with a remote MCP server via WebSocket
type MCPWebSocketClient struct {
	name    string
	url     string
	headers map[string]string

	// WebSocket connection
	conn   *websocket.Conn
	connMu sync.Mutex

	// State
	mu          sync.RWMutex
	initialized bool
	tools       []api.Tool
	requestID   int64
	responses   map[int64]chan *jsonRPCResponse

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMCPWebSocketClient creates a new WebSocket-based MCP client
func NewMCPWebSocketClient(name, url string, headers map[string]string) *MCPWebSocketClient {
	ctx, cancel := context.WithCancel(context.Background())

	return &MCPWebSocketClient{
		name:      name,
		url:       url,
		headers:   headers,
		responses: make(map[int64]chan *jsonRPCResponse),
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
}

// Start establishes the WebSocket connection to the MCP server
func (c *MCPWebSocketClient) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return errors.New("MCP WebSocket client already started")
	}

	slog.Info("Connecting to MCP WebSocket server", "name", c.name, "url", c.url)

	// Create dialer with custom headers
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// Build HTTP headers
	header := http.Header{}
	for k, v := range c.headers {
		header.Set(k, v)
	}

	// Connect
	conn, resp, err := dialer.DialContext(c.ctx, c.url, header)
	if err != nil {
		if resp != nil {
			slog.Error("WebSocket connection failed", "name", c.name, "status", resp.StatusCode, "error", err)
		}
		return fmt.Errorf("failed to connect to MCP server %s: %w", c.name, err)
	}

	c.conn = conn

	// Start response handler
	go c.handleResponses()

	slog.Info("MCP WebSocket connection established", "name", c.name)
	return nil
}

// Initialize performs MCP protocol initialization
func (c *MCPWebSocketClient) Initialize() error {
	c.mu.RLock()
	if c.initialized {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	slog.Debug("Initializing MCP WebSocket client", "name", c.name)

	// Send initialize request
	initReq := mcpInitializeRequest{
		ProtocolVersion: "2024-11-05",
		Capabilities: map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		ClientInfo: mcpClientInfo{
			Name:    "ollama",
			Version: "1.0.0",
		},
	}

	var initResult mcpInitializeResponse
	if err := c.call("initialize", initReq, &initResult); err != nil {
		return fmt.Errorf("MCP initialize failed: %w", err)
	}

	slog.Debug("MCP server initialized",
		"name", c.name,
		"serverName", initResult.ServerInfo.Name,
		"serverVersion", initResult.ServerInfo.Version,
		"protocolVersion", initResult.ProtocolVersion)

	// Send initialized notification
	if err := c.notify("notifications/initialized", nil); err != nil {
		slog.Warn("Failed to send initialized notification", "name", c.name, "error", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()

	return nil
}

// ListTools retrieves the list of available tools from the server
func (c *MCPWebSocketClient) ListTools() ([]api.Tool, error) {
	var result mcpListToolsResponse
	if err := c.call("tools/list", nil, &result); err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	tools := make([]api.Tool, 0, len(result.Tools))
	for _, mcpTool := range result.Tools {
		tool := api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        mcpTool.Name,
				Description: mcpTool.Description,
			},
		}

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

	slog.Debug("Listed MCP tools", "name", c.name, "count", len(tools))
	return tools, nil
}

// CallTool invokes a tool on the MCP server
func (c *MCPWebSocketClient) CallTool(name string, args map[string]interface{}) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
	defer cancel()

	callReq := struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments,omitempty"`
	}{
		Name:      name,
		Arguments: args,
	}

	var result mcpCallToolResponse
	if err := c.callWithContext(ctx, "tools/call", callReq, &result); err != nil {
		return "", fmt.Errorf("tool call failed: %w", err)
	}

	// Check if tool returned an error
	if result.IsError {
		var errMsg string
		for _, content := range result.Content {
			if content.Type == "text" {
				errMsg += content.Text
			}
		}
		slog.Error("MCP tool execution error", "name", name, "content_count", len(result.Content))
		return errMsg, fmt.Errorf("MCP tool returned error")
	}

	// Extract text content from result
	var textContent []string
	for _, content := range result.Content {
		if content.Type == "text" {
			textContent = append(textContent, content.Text)
		}
	}

	return joinStrings(textContent, "\n"), nil
}

// GetTools returns the cached list of tools
func (c *MCPWebSocketClient) GetTools() []api.Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools
}

// Close shuts down the WebSocket connection
func (c *MCPWebSocketClient) Close() error {
	slog.Info("Shutting down MCP WebSocket client", "name", c.name)

	c.cancel()

	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn != nil {
		// Send close message
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.conn.Close()
		c.conn = nil
	}

	close(c.done)
	return nil
}

// call sends a JSON-RPC request and waits for the response
func (c *MCPWebSocketClient) call(method string, params interface{}, result interface{}) error {
	return c.callWithContext(c.ctx, method, params, result)
}

func (c *MCPWebSocketClient) callWithContext(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&c.requestID, 1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	// Create response channel
	respChan := make(chan *jsonRPCResponse, 1)
	c.mu.Lock()
	c.responses[id] = respChan
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.responses, id)
		c.mu.Unlock()
	}()

	// Send request
	if err := c.sendRequest(req); err != nil {
		return err
	}

	// Wait for response
	select {
	case resp := <-respChan:
		if resp.Error != nil {
			return fmt.Errorf("JSON-RPC error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("failed to unmarshal result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// notify sends a JSON-RPC notification (no response expected)
func (c *MCPWebSocketClient) notify(method string, params interface{}) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.sendRequest(req)
}

// sendRequest sends a JSON-RPC request over WebSocket
func (c *MCPWebSocketClient) sendRequest(req jsonRPCRequest) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == nil {
		return errors.New("WebSocket connection not established")
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	slog.Debug("Sending MCP WebSocket request", "name", c.name, "method", req.Method)

	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("failed to send WebSocket message: %w", err)
	}

	return nil
}

// handleResponses reads incoming WebSocket messages and routes them
func (c *MCPWebSocketClient) handleResponses() {
	defer func() {
		slog.Debug("MCP WebSocket response handler exiting", "name", c.name)
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("MCP WebSocket closed normally", "name", c.name)
			} else if !errors.Is(err, context.Canceled) {
				slog.Error("Error reading MCP WebSocket message", "name", c.name, "error", err)
			}
			return
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(message, &resp); err != nil {
			slog.Warn("Failed to parse MCP WebSocket response", "name", c.name, "error", err)
			continue
		}

		// Route response to waiting caller
		if resp.ID != nil {
			c.mu.RLock()
			respChan, ok := c.responses[*resp.ID]
			c.mu.RUnlock()

			if ok {
				select {
				case respChan <- &resp:
				default:
					slog.Warn("Response channel full, dropping response", "name", c.name, "id", *resp.ID)
				}
			} else {
				slog.Warn("Received response for unknown request ID", "name", c.name, "id", *resp.ID)
			}
		}
	}
}

// joinStrings joins strings with a separator (utility function)
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
