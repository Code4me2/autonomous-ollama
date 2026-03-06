package server

// routes_mcp_chat.go — MCP integration for the ChatHandler.
//
// Extracted from routes.go to minimise merge conflicts with upstream.
// All MCP-specific chat logic lives here; routes.go calls into it via:
//   - setupMCPForChat()  → manager init, tool discovery, context injection
//   - runMCPToolLoop()   → multi-round tool execution goroutine
//   - collectNonStreamingMCP() → non-streaming response accumulation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/model/parsers"
	"github.com/ollama/ollama/thinking"
	"github.com/ollama/ollama/tools"
	"github.com/ollama/ollama/types/model"
)

// ---------------------------------------------------------------------------
// CompletionResult — shared response accumulator
// ---------------------------------------------------------------------------

// CompletionResult holds the result of a completion request
type CompletionResult struct {
	Content    string
	Thinking   string
	ToolCalls  []api.ToolCall
	Done       bool
	DoneReason string
	Metrics    api.Metrics
	Error      error
}

// ---------------------------------------------------------------------------
// looksLikeFailedToolCall
// ---------------------------------------------------------------------------

// looksLikeFailedToolCall checks if content appears to be a malformed tool call attempt.
// This detects when the model output something resembling a tool call but the parser
// didn't recognize it (e.g., missing [TOOL_CALLS] prefix for ministral format).
func looksLikeFailedToolCall(content string) bool {
	// Check for common tool call patterns without proper prefix
	// Pattern: word[ARGS]{ or word[ARGS] {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}

	// Look for toolname[ARGS]{...} pattern
	argsIdx := strings.Index(content, "[ARGS]")
	if argsIdx > 0 && argsIdx < 50 { // Tool name should be reasonably short
		// Check that there's a potential tool name before [ARGS]
		potentialName := strings.TrimSpace(content[:argsIdx])
		if len(potentialName) > 0 && !strings.ContainsAny(potentialName, " \t\n") {
			// Check for JSON object after [ARGS]
			afterArgs := content[argsIdx+6:] // len("[ARGS]") = 6
			afterArgs = strings.TrimSpace(afterArgs)
			if strings.HasPrefix(afterArgs, "{") {
				return true
			}
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// executeCompletionWithTools
// ---------------------------------------------------------------------------

// executeCompletionWithTools executes a completion and collects the full response.
// This is a synchronous wrapper around the async completion callback.
// When suppressDone is true, the Done flag is not sent to the client channel
// (used for intermediate rounds in multi-round tool execution).
// When suppressStreaming is true, content is not streamed to the client
// (used for retry rounds after failed tool call detection).
func (s *Server) executeCompletionWithTools(
	ctx context.Context,
	r llm.LlamaServer,
	prompt string,
	images []llm.ImageData,
	opts *api.Options,
	req api.ChatRequest,
	m *Model,
	builtinParser parsers.Parser,
	thinkingState *thinking.Parser,
	ch chan any,
	checkpointStart time.Time,
	checkpointLoaded time.Time,
	truncate bool,
	suppressDone bool,
	suppressStreaming bool,
) (*CompletionResult, error) {
	result := &CompletionResult{}
	done := make(chan error, 1)

	// For tracking tool calls when using tools
	var toolParser *tools.Parser
	if len(req.Tools) > 0 && builtinParser == nil {
		toolParser = tools.NewParser(m.Template.Template, req.Tools)
	}

	// Track thinking content for structured outputs
	var thinkingBuilder strings.Builder

	// Accumulate tool calls across streaming chunks
	var accumulatedToolCalls []api.ToolCall

	// Create a new context for this completion
	completionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	err := r.Completion(completionCtx, llm.CompletionRequest{
		Prompt:      prompt,
		Images:      images,
		Format:      req.Format,
		Options:     opts,
		Shift:       req.Shift == nil || *req.Shift,
		Truncate:    truncate,
		Logprobs:    req.Logprobs,
		TopLogprobs: req.TopLogprobs,
	}, func(resp llm.CompletionResponse) {
		// When suppressDone is true, don't signal Done to client
		// (used for intermediate rounds in multi-round tool execution)
		clientDone := resp.Done && !suppressDone

		// Determine task status for A2A compatibility
		taskStatus := "working"
		if clientDone {
			taskStatus = "completed"
		}

		res := api.ChatResponse{
			Model:     req.Model,
			CreatedAt: time.Now().UTC(),
			Message:   api.Message{Role: "assistant", Content: resp.Content},
			Done:      clientDone,
			TaskID:    req.TaskID,
			TaskStatus: taskStatus,
			Metrics: api.Metrics{
				PromptEvalCount:    resp.PromptEvalCount,
				PromptEvalDuration: resp.PromptEvalDuration,
				EvalCount:          resp.EvalCount,
				EvalDuration:       resp.EvalDuration,
			},
			Logprobs: toAPILogprobs(resp.Logprobs),
		}

		if resp.Done {
			res.DoneReason = resp.DoneReason.String()
			res.TotalDuration = time.Since(checkpointStart)
			res.LoadDuration = checkpointLoaded.Sub(checkpointStart)
			result.DoneReason = res.DoneReason
			result.Metrics = res.Metrics
		}

		// Handle builtin parser (for models with native tool support)
		if builtinParser != nil {
			content, thinking, toolCalls, err := builtinParser.Add(resp.Content, resp.Done)
			if err != nil {
				result.Error = err
				done <- err
				cancel() // Stop the completion to prevent Done callback deadlock
				return
			}

			// Assign IDs to tool calls that don't have them (Quirk 4 fix)
			for i := range toolCalls {
				if toolCalls[i].ID == "" {
					toolCalls[i].ID = toolCallId()
				}
			}

			res.Message.Content = content
			res.Message.Thinking = thinking
			res.Message.ToolCalls = toolCalls

			thinkingBuilder.WriteString(thinking)

			// Accumulate results
			result.Content += content
			result.Thinking += thinking

			// Accumulate tool calls for multi-round MCP execution
			if len(toolCalls) > 0 {
				accumulatedToolCalls = append(accumulatedToolCalls, toolCalls...)
			}

			// On completion, set all accumulated tool calls
			if resp.Done {
				result.ToolCalls = accumulatedToolCalls
			}

			// Stream to client if there's content to stream (unless suppressed for retry)
			if !suppressStreaming {
				if res.Message.Content != "" || res.Message.Thinking != "" || len(res.Message.ToolCalls) > 0 || resp.Done || len(res.Logprobs) > 0 {
					ch <- res
				}
			}

			if resp.Done {
				result.Done = true
				done <- nil
			}
			return
		}

		// Handle thinking state parser
		if thinkingState != nil {
			thinkingContent, remainingContent := thinkingState.AddContent(res.Message.Content)
			if thinkingContent == "" && remainingContent == "" && !resp.Done {
				// Need more content to decide
				return
			}

			res.Message.Thinking = thinkingContent
			thinkingBuilder.WriteString(thinkingContent)
			res.Message.Content = remainingContent
			result.Thinking += thinkingContent
		}

		// Handle tool parsing (for models without native tool support)
		if len(req.Tools) > 0 && builtinParser == nil {
			toolCalls, content := toolParser.Add(res.Message.Content)
			// Assign IDs to tool calls that don't have them (Quirk 4 fix)
			for i := range toolCalls {
				if toolCalls[i].ID == "" {
					toolCalls[i].ID = toolCallId()
				}
			}
			if len(content) > 0 {
				res.Message.Content = content
				result.Content += content
			} else if len(toolCalls) > 0 {
				res.Message.ToolCalls = toolCalls
				res.Message.Content = ""
				// Keep accumulating tool calls
				accumulatedToolCalls = toolCalls
			} else if res.Message.Thinking != "" {
				// don't return, fall through to send
			} else {
				// Send logprobs while content is being buffered by the parser for tool calls
				if len(res.Logprobs) > 0 && !resp.Done && !suppressStreaming {
					logprobRes := res
					logprobRes.Message.Content = ""
					logprobRes.Message.ToolCalls = nil
					ch <- logprobRes
				}

				if resp.Done {
					res.Message.Content = toolParser.Content()
					// Set accumulated tool calls in result before signaling done
					if len(accumulatedToolCalls) > 0 {
						result.ToolCalls = accumulatedToolCalls
					}
					// If no tool calls, get final content from parser
					if len(result.ToolCalls) == 0 && toolParser != nil {
						result.Content = toolParser.Content()
					}
					result.Done = true
					if !suppressStreaming {
						ch <- res
					}
					done <- nil
				}
				return
			}
		} else {
			result.Content += res.Message.Content
		}

		// Stream to client (unless suppressed for retry)
		if !suppressStreaming {
			ch <- res
		}

		if resp.Done {
			// If we accumulated tool calls, set them in result
			if len(accumulatedToolCalls) > 0 {
				result.ToolCalls = accumulatedToolCalls
			}
			// If no tool calls, get final content from parser
			if len(result.ToolCalls) == 0 && toolParser != nil {
				result.Content = toolParser.Content()
			}
			result.Done = true
			done <- nil
		}
	})

	// If the parser triggered a cancel (e.g. unknown tool name), report
	// its error instead of the resulting context.Canceled from r.Completion.
	if result.Error != nil {
		return nil, result.Error
	}

	if err != nil {
		return nil, err
	}

	// Wait for completion or context cancellation
	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// MCPChatState — holds MCP state threaded through ChatHandler
// ---------------------------------------------------------------------------

// MCPChatState carries MCP state from setupMCPForChat into the tool loop.
type MCPChatState struct {
	Manager        *MCPManager
	ProcessedTools []api.Tool
}

// ---------------------------------------------------------------------------
// setupMCPForChat — manager init, discovery, context injection
// ---------------------------------------------------------------------------

// setupMCPForChat initialises the MCP manager, discovers tools, injects
// JIT context, and configures the parser. It modifies req in place.
// Returns nil MCPChatState if no MCP servers are configured.
func setupMCPForChat(req *api.ChatRequest, m *Model, caps []model.Capability) (*MCPChatState, []model.Capability) {
	servers, err := ResolveServersForRequest(*req)
	if err != nil {
		slog.Warn("Failed to resolve servers", "error", err)
	}
	if len(servers) == 0 {
		return nil, caps
	}

	sessionID := GenerateSessionID(*req)
	mcpManager, err := GetMCPManager(sessionID, servers, req.JITMaxTools)
	if err != nil {
		slog.Error("Failed to create MCP manager", "error", err)
		return nil, caps
	}

	slog.Info("MCP manager created",
		"session", sessionID,
		"pending_servers", mcpManager.GetPendingServerCount(),
		"max_tools_per_discovery", mcpManager.GetMaxToolsPerDiscovery(),
		"tools_path", req.ToolsPath)

	// If the session already discovered tools (from a prior request),
	// include them so the parser recognises tool calls the model may
	// generate based on conversation history.  For fresh sessions this
	// returns just mcp_discover.
	req.Tools = append(req.Tools, mcpManager.GetActiveTools()...)
	slog.Info("MCP: Initial tools from session",
		"count", len(req.Tools),
		"discovered", mcpManager.GetDiscoveredToolCount())

	// Inject context explaining mcp_discover and working directory
	codeAPI := NewMCPCodeAPI(mcpManager)
	req.Messages = codeAPI.InjectJITContext(req.Messages, servers)

	// Auto-configure parser for tool call detection
	if len(req.Tools) > 0 && m.Config.Parser == "" {
		if m.Config.ModelFamily == "qwen2" || m.Config.ModelFamily == "qwen3" {
			m.Config.Parser = "qwen3-vl-instruct"
		}
	}

	// Update capabilities now that we have tools
	if len(req.Tools) > 0 {
		hasToolCap := false
		for _, c := range caps {
			if c == model.CapabilityTools {
				hasToolCap = true
				break
			}
		}
		if !hasToolCap {
			caps = append(caps, model.CapabilityTools)
		}
	}

	// Note: MCP manager is NOT closed here - it's managed by the session manager
	// which handles cleanup via TTL expiration. This allows session reuse across
	// multiple requests with the same session_id.

	return &MCPChatState{
		Manager:        mcpManager,
		ProcessedTools: req.Tools,
	}, caps
}

// ---------------------------------------------------------------------------
// runMCPToolLoop — the multi-round tool execution goroutine body
// ---------------------------------------------------------------------------

// MCPToolLoopParams bundles the values the tool loop needs from ChatHandler.
type MCPToolLoopParams struct {
	Server           *Server
	Ctx              context.Context
	Runner           llm.LlamaServer
	Model            *Model
	Req              api.ChatRequest
	Opts             *api.Options
	Msgs             []api.Message
	ProcessedTools   []api.Tool
	BuiltinParser    parsers.Parser
	ThinkingState    *thinking.Parser
	Prompt           string
	Images           []llm.ImageData
	Truncate         bool
	CheckpointStart  time.Time
	CheckpointLoaded time.Time
	Ch               chan any
	MCPManager       *MCPManager
}

// runMCPToolLoop runs the multi-round tool execution loop.
// It writes responses to p.Ch and returns when the model stops calling tools
// or max rounds are reached. Caller is responsible for closing p.Ch.
func runMCPToolLoop(p MCPToolLoopParams) {
	currentMsgs := p.Msgs
	prompt := p.Prompt
	images := p.Images
	processedTools := p.ProcessedTools
	builtinParser := p.BuiltinParser

	maxRounds := p.Req.MaxToolRounds
	if maxRounds == 0 {
		maxRounds = 15
	}

	slog.Debug("Starting multi-round execution",
		"mcpManager", p.MCPManager != nil,
		"tools_count", len(p.Req.Tools),
		"max_rounds", maxRounds)

	var round int
	var retryingFailedToolCall bool

	for round = 0; round < maxRounds; round++ {
		slog.Debug("Starting tool round", "round", round, "messages", len(currentMsgs), "tools", len(processedTools))

		// Re-render prompt and reset parser if not first round (tool results were added)
		if round > 0 {
			currentTools := processedTools
			if p.MCPManager != nil {
				currentTools = p.MCPManager.GetActiveTools()
				processedTools = currentTools
				slog.Info("MCP: Re-rendering with updated tools", "round", round, "tools", len(currentTools))
			}

			var err error
			prompt, images, err = chatPrompt(p.Ctx, p.Model, p.Runner.Tokenize, p.Opts, currentMsgs, currentTools, p.Req.Think, p.Truncate)
			if err != nil {
				slog.Error("Failed to render prompt in round", "round", round, "error", err)
				p.Ch <- gin.H{"error": err.Error()}
				return
			}

			var toolNames []string
			for _, t := range currentTools {
				toolNames = append(toolNames, t.Function.Name)
			}
			slog.Info("JIT: Prompt re-rendered", "round", round, "prompt_length", len(prompt), "tool_names", toolNames)

			// Create fresh parser instance for new round (parser has internal buffer state)
			if builtinParser != nil && p.Model.Config.Parser != "" {
				builtinParser = parsers.ParserForName(p.Model.Config.Parser)
				if builtinParser != nil {
					lastMsg := &currentMsgs[len(currentMsgs)-1]
					builtinParser.Init(currentTools, lastMsg, p.Req.Think)
					slog.Info("JIT: Parser re-initialized", "parser", p.Model.Config.Parser, "tools_count", len(currentTools))
				}
			} else {
				slog.Warn("JIT: No builtin parser available", "parser_name", p.Model.Config.Parser, "builtin_nil", builtinParser == nil)
			}
		}

		// Execute completion — always suppress Done during the loop
		suppressDone := true
		slog.Debug("Calling executeCompletionWithTools", "round", round, "prompt_len", len(prompt), "suppress_done", suppressDone, "suppress_streaming", retryingFailedToolCall)
		completionResult, err := p.Server.executeCompletionWithTools(
			p.Ctx,
			p.Runner,
			prompt,
			images,
			p.Opts,
			p.Req,
			p.Model,
			builtinParser,
			p.ThinkingState,
			p.Ch,
			p.CheckpointStart,
			p.CheckpointLoaded,
			p.Truncate,
			suppressDone,
			retryingFailedToolCall,
		)

		if err != nil {
			slog.Error("Completion failed", "round", round, "error", err)
			var serr api.StatusError
			if errors.As(err, &serr) {
				p.Ch <- gin.H{"error": serr.ErrorMessage, "status": serr.StatusCode}
			} else {
				p.Ch <- gin.H{"error": err.Error()}
			}
			return
		}

		// Check if model called tools
		if len(completionResult.ToolCalls) == 0 {
			if looksLikeFailedToolCall(completionResult.Content) {
				slog.Warn("Detected failed tool call attempt, re-prompting",
					"round", round,
					"content", completionResult.Content)

				currentMsgs = append(currentMsgs, api.Message{
					Role:    "assistant",
					Content: completionResult.Content,
				})
				currentMsgs = append(currentMsgs, api.Message{
					Role:    "user",
					Content: "Your tool call was not recognized. Please use the exact format: [TOOL_CALLS]tool_name[ARGS]{\"argument\": \"value\"}",
				})
				retryingFailedToolCall = true
				continue
			}

			slog.Info("No tools called, conversation complete", "round", round, "content_length", len(completionResult.Content))
			break
		}

		retryingFailedToolCall = false

		// Validate tool calls
		validToolCalls := 0
		for _, tc := range completionResult.ToolCalls {
			if tc.Function.Name != "" {
				validToolCalls++
			} else {
				slog.Warn("Invalid tool call detected", "round", round, "tool", tc)
			}
		}
		if validToolCalls == 0 {
			slog.Warn("No valid tool calls found, exiting", "round", round)
			break
		}

		// Model called tools — execute them
		if p.MCPManager != nil {
			executeMCPToolCalls(p, completionResult, &currentMsgs, &processedTools, round)
		} else {
			executeNoMCPFallback(p, completionResult, &currentMsgs, round)
		}
	}

	// Check if we exhausted rounds
	if round >= maxRounds {
		slog.Warn("Maximum tool execution rounds reached", "rounds", maxRounds)
		p.Ch <- gin.H{"error": fmt.Sprintf("Maximum tool execution rounds (%d) exceeded", maxRounds)}
	}

	// Send final Done: true
	p.Ch <- api.ChatResponse{
		Model:      p.Req.Model,
		CreatedAt:  time.Now().UTC(),
		Message:    api.Message{Role: "assistant"},
		Done:       true,
		DoneReason: "stop",
		TaskID:     p.Req.TaskID,
		TaskStatus: "completed",
	}
}

// ---------------------------------------------------------------------------
// executeMCPToolCalls — handle tool calls when an MCP manager is present
// ---------------------------------------------------------------------------

func executeMCPToolCalls(
	p MCPToolLoopParams,
	completionResult *CompletionResult,
	currentMsgs *[]api.Message,
	processedTools *[]api.Tool,
	round int,
) {
	slog.Debug("MCP tool execution starting",
		"tools_in_response", len(completionResult.ToolCalls),
		"round", round)

	// Separate discovery calls from regular tool calls
	var discoveryResults []api.ToolResult
	var regularToolCalls []api.ToolCall

	for _, toolCall := range completionResult.ToolCalls {
		if IsMCPDiscoverCall(toolCall) {
			// Tier 1: Server catalog (no tool injection)
			pattern, _ := toolCall.Function.Arguments.Get("pattern")
			patternStr, _ := pattern.(string)
			if patternStr == "" {
				patternStr = "*"
			}

			summary, err := p.MCPManager.HandleDiscovery(patternStr)
			if err != nil {
				discoveryResults = append(discoveryResults, api.ToolResult{
					ToolName:  "mcp_discover",
					Arguments: toolCall.Function.Arguments,
					Content:   fmt.Sprintf("Discovery error: %v", err),
					Error:     err.Error(),
				})
				continue
			}

			discoveryResults = append(discoveryResults, api.ToolResult{
				ToolName:  "mcp_discover",
				Arguments: toolCall.Function.Arguments,
				Content:   summary,
			})

		} else if IsMCPListToolsCall(toolCall) {
			// Tier 2: Load tools from specific servers
			serversRaw, _ := toolCall.Function.Arguments.Get("servers")
			var serverNames []string
			if serversSlice, ok := serversRaw.([]interface{}); ok {
				for _, s := range serversSlice {
					if name, ok := s.(string); ok {
						serverNames = append(serverNames, name)
					}
				}
			}

			if len(serverNames) == 0 {
				discoveryResults = append(discoveryResults, api.ToolResult{
					ToolName:  "mcp_list_tools",
					Arguments: toolCall.Function.Arguments,
					Content:   "Error: 'servers' must be a non-empty array of server names. Use mcp_discover first to see available servers.",
					Error:     "missing or empty servers parameter",
				})
				continue
			}

			newTools, summary, err := p.MCPManager.HandleListTools(serverNames)
			if err != nil {
				discoveryResults = append(discoveryResults, api.ToolResult{
					ToolName:  "mcp_list_tools",
					Arguments: toolCall.Function.Arguments,
					Content:   fmt.Sprintf("Error loading tools: %v", err),
					Error:     err.Error(),
				})
				continue
			}

			// Inject discovered tools for next round
			if len(newTools) > 0 {
				*processedTools = append(*processedTools, newTools...)
				slog.Info("JIT: Injected tools from mcp_list_tools",
					"servers", serverNames,
					"count", len(newTools),
					"total_active", len(*processedTools))
			}

			discoveryResults = append(discoveryResults, api.ToolResult{
				ToolName:  "mcp_list_tools",
				Arguments: toolCall.Function.Arguments,
				Content:   summary,
			})

		} else {
			regularToolCalls = append(regularToolCalls, toolCall)
		}
	}

	// If we had discovery/list_tools calls, add to context
	if len(discoveryResults) > 0 {
		p.Ch <- api.ChatResponse{
			Model:      p.Req.Model,
			TaskID:     p.Req.TaskID,
			TaskStatus: "working",
			Message: api.Message{
				Role:        "assistant",
				ToolResults: discoveryResults,
			},
		}

		// Collect discovery tool calls from the completion
		var discoveryToolCalls []api.ToolCall
		for _, toolCall := range completionResult.ToolCalls {
			if IsMCPDiscoverCall(toolCall) || IsMCPListToolsCall(toolCall) {
				discoveryToolCalls = append(discoveryToolCalls, toolCall)
			}
		}

		// Add assistant message with discovery tool calls BEFORE tool results
		if len(discoveryToolCalls) > 0 {
			*currentMsgs = append(*currentMsgs, api.Message{
				Role:      "assistant",
				Content:   completionResult.Content,
				ToolCalls: discoveryToolCalls,
			})
		}

		// Add discovery tool result messages
		for _, dr := range discoveryResults {
			*currentMsgs = append(*currentMsgs, api.Message{
				Role:    "tool",
				Content: dr.Content,
			})
		}

		slog.Info("JIT: Discovery/list_tools complete",
			"results", len(discoveryResults),
			"messages", len(*currentMsgs))
	}

	// If ONLY discovery/list_tools happened (no regular tools), return to continue loop
	if len(regularToolCalls) == 0 {
		if len(discoveryResults) > 0 {
			slog.Info("JIT: Only discovery/list_tools calls, continuing to next round",
				"round", round,
				"discovered_tools", p.MCPManager.GetDiscoveredToolCount())
		}
		return
	}

	// Analyze execution plan and execute regular tools
	executionPlan := p.MCPManager.AnalyzeExecutionPlan(regularToolCalls)
	slog.Debug("Execution plan determined",
		"sequential", executionPlan.RequiresSequential,
		"reason", executionPlan.Reason)

	results := p.MCPManager.ExecuteWithPlan(regularToolCalls, executionPlan)

	for i, tc := range regularToolCalls {
		slog.Info("Tool call details",
			"round", round,
			"index", i,
			"name", tc.Function.Name,
			"arguments", tc.Function.Arguments)
	}

	// Add assistant message with tool calls
	if len(discoveryResults) == 0 {
		*currentMsgs = append(*currentMsgs, api.Message{
			Role:      "assistant",
			Content:   completionResult.Content,
			ToolCalls: regularToolCalls,
		})
	} else {
		*currentMsgs = append(*currentMsgs, api.Message{
			Role:      "assistant",
			ToolCalls: regularToolCalls,
		})
	}

	// Add tool result messages and send them to client for display
	toolResultsForDisplay := make([]api.ToolResult, 0, len(results))
	for i, result := range results {
		toolMsg := api.Message{
			Role:     "tool",
			ToolName: regularToolCalls[i].Function.Name,
		}

		displayResult := api.ToolResult{
			ToolName:  regularToolCalls[i].Function.Name,
			Arguments: regularToolCalls[i].Function.Arguments,
			Content:   result.Content,
		}

		if result.Error != nil {
			if encoded, err := json.Marshal(fmt.Sprintf("Error: %v", result.Error)); err == nil {
				toolMsg.Content = string(encoded)
			} else {
				toolMsg.Content = fmt.Sprintf("\"Error: %v\"", result.Error)
			}
			displayResult.Error = result.Error.Error()
			slog.Warn("Tool execution failed",
				"tool", regularToolCalls[i].Function.Name,
				"error", result.Error)
		} else {
			if encoded, err := json.Marshal(result.Content); err == nil {
				toolMsg.Content = string(encoded)
			} else {
				toolMsg.Content = result.Content
			}
		}

		*currentMsgs = append(*currentMsgs, toolMsg)
		toolResultsForDisplay = append(toolResultsForDisplay, displayResult)
	}

	if len(toolResultsForDisplay) > 0 {
		p.Ch <- api.ChatResponse{
			Model:      p.Req.Model,
			TaskID:     p.Req.TaskID,
			TaskStatus: "working",
			Message: api.Message{
				Role:        "assistant",
				ToolResults: toolResultsForDisplay,
			},
		}
	}

	slog.Info("Tools executed, continuing to next round",
		"round", round,
		"messages", len(*currentMsgs),
		"last_tool", regularToolCalls[len(regularToolCalls)-1].Function.Name)
}

// ---------------------------------------------------------------------------
// executeNoMCPFallback — tool calls without an MCP manager
// ---------------------------------------------------------------------------

func executeNoMCPFallback(
	p MCPToolLoopParams,
	completionResult *CompletionResult,
	currentMsgs *[]api.Message,
	round int,
) {
	slog.Warn("Tool calls made but no MCP manager available", "round", round, "tool_count", len(completionResult.ToolCalls))

	var errorResults []api.ToolResult
	for _, tc := range completionResult.ToolCalls {
		errorResults = append(errorResults, api.ToolResult{
			ToolName:  tc.Function.Name,
			Arguments: tc.Function.Arguments,
			Error:     fmt.Sprintf("Tool '%s' is not available. No MCP servers are configured.", tc.Function.Name),
		})
	}

	if len(errorResults) > 0 {
		p.Ch <- api.ChatResponse{
			Model:      p.Req.Model,
			TaskID:     p.Req.TaskID,
			TaskStatus: "working",
			Message: api.Message{
				Role:        "assistant",
				ToolCalls:   completionResult.ToolCalls,
				ToolResults: errorResults,
			},
		}
	}

	*currentMsgs = append(*currentMsgs, api.Message{
		Role:      "assistant",
		Content:   completionResult.Content,
		ToolCalls: completionResult.ToolCalls,
	})

	for _, result := range errorResults {
		*currentMsgs = append(*currentMsgs, api.Message{
			Role:     "tool",
			ToolName: result.ToolName,
			Content:  fmt.Sprintf("Error: %s", result.Error),
		})
	}

	slog.Info("Tool errors fed back to model, continuing", "round", round)
}

// ---------------------------------------------------------------------------
// collectNonStreamingMCP — non-streaming response accumulation with MCP fields
// ---------------------------------------------------------------------------

// collectNonStreamingMCP drains ch and assembles a single ChatResponse,
// accumulating tool calls and tool results from MCP rounds.
func collectNonStreamingMCP(ch <-chan any, req api.ChatRequest) (api.ChatResponse, error) {
	var resp api.ChatResponse
	var toolCalls []api.ToolCall
	var toolResults []api.ToolResult
	var allLogprobs []api.Logprob
	var sbThinking strings.Builder
	var sbContent strings.Builder

	hasMCPOrTools := len(req.Tools) > 0 || len(req.MCPServers) > 0

	for rr := range ch {
		switch t := rr.(type) {
		case api.ChatResponse:
			sbThinking.WriteString(t.Message.Thinking)
			sbContent.WriteString(t.Message.Content)
			resp = t
			if hasMCPOrTools {
				toolCalls = append(toolCalls, t.Message.ToolCalls...)
				toolResults = append(toolResults, t.Message.ToolResults...)
			}
			if len(t.Logprobs) > 0 {
				allLogprobs = append(allLogprobs, t.Logprobs...)
			}
		case gin.H:
			msg, ok := t["error"].(string)
			if !ok {
				msg = "unexpected error"
			}
			return api.ChatResponse{}, fmt.Errorf("%s", msg)
		default:
			return api.ChatResponse{}, fmt.Errorf("unexpected response type")
		}
	}

	resp.Message.Content = sbContent.String()
	resp.Message.Thinking = sbThinking.String()
	resp.Logprobs = allLogprobs

	if len(toolCalls) > 0 {
		resp.Message.ToolCalls = toolCalls
	}
	if req.IncludeToolResults && len(toolResults) > 0 {
		resp.ToolResults = toolResults
	}

	return resp, nil
}
