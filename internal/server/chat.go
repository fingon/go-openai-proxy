package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fingon/go-openai-proxy/internal/codex"
	"github.com/fingon/go-openai-proxy/internal/sse"
)

type chatRequest struct {
	MaxTokens         int           `json:"max_tokens,omitempty"`
	Messages          []chatMessage `json:"messages"`
	Model             string        `json:"model,omitempty"`
	ParallelToolCalls *bool         `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string        `json:"reasoning_effort,omitempty"`
	Stop              any           `json:"stop,omitempty"`
	Stream            bool          `json:"stream,omitempty"`
	Temperature       *float64      `json:"temperature,omitempty"`
	ToolChoice        any           `json:"tool_choice,omitempty"`
	Tools             []chatTool    `json:"tools,omitempty"`
	TopP              *float64      `json:"top_p,omitempty"`
}

type chatMessage struct {
	Content    any            `json:"content,omitempty"`
	Role       string         `json:"role,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Function chatToolFunction `json:"function,omitempty"`
	Type     string           `json:"type,omitempty"`
}

type chatToolFunction struct {
	Description string         `json:"description,omitempty"`
	Name        string         `json:"name,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Function chatToolCallFunction `json:"function,omitempty"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
}

type chatToolCallFunction struct {
	Arguments string `json:"arguments,omitempty"`
	Name      string `json:"name,omitempty"`
}

func (handler *Handler) handleChatCompletions(responseWriter http.ResponseWriter, request *http.Request) {
	body, err := readRequestBody(request)
	if err != nil {
		writeError(responseWriter, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	var chat chatRequest
	if err := json.Unmarshal(body, &chat); err != nil || chat.Messages == nil {
		writeError(responseWriter, http.StatusBadRequest, "`messages` must be an array.", "invalid_request_error")
		return
	}

	responsesPayload := chat.toResponsesPayload()
	encoded, err := json.Marshal(codex.NormalizeResponsesPayload(responsesPayload, codex.NormalizeOptions{ForceStream: true}))
	if err != nil {
		writeError(responseWriter, http.StatusInternalServerError, "Failed to encode request.", "server_error")
		return
	}

	upstream, err := handler.codexClient.RawRequest(request.Context(), http.MethodPost, "/responses", jsonContentHeader(), encoded)
	if err != nil {
		writeError(responseWriter, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	defer closeBody(upstream.Body, "upstream chat body")

	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		copyUpstreamResponse(responseWriter, upstream)
		return
	}
	if chat.Stream {
		handler.streamChatResponse(responseWriter, upstream, chat)
		return
	}

	completed, err := sse.CollectCompletedResponse(upstream.Body)
	if err != nil {
		writeError(responseWriter, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	writeJSON(responseWriter, http.StatusOK, toChatCompletion(completed, chat))
}

func (chat chatRequest) toResponsesPayload() map[string]any {
	model := chat.Model
	if model == "" {
		model = defaultChatModel
	}

	payload := map[string]any{
		"input": chat.toResponsesInput(),
		"model": model,
	}
	if len(chat.Tools) > 0 {
		payload["tools"] = chat.toResponsesTools()
	}
	if chat.ToolChoice != nil {
		payload["tool_choice"] = chat.ToolChoice
	}
	if chat.Temperature != nil {
		payload["temperature"] = *chat.Temperature
	}
	if chat.TopP != nil {
		payload["top_p"] = *chat.TopP
	}
	if chat.Stop != nil {
		payload["stop"] = chat.Stop
	}
	if chat.MaxTokens > 0 {
		payload["max_output_tokens"] = chat.MaxTokens
	}
	if chat.ParallelToolCalls != nil {
		payload["parallel_tool_calls"] = *chat.ParallelToolCalls
	}
	if chat.ReasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": chat.ReasoningEffort}
	}

	return payload
}

func (chat chatRequest) toResponsesInput() []any {
	input := make([]any, 0, len(chat.Messages))
	toolNamesByID := make(map[string]string)
	for _, message := range chat.Messages {
		switch message.Role {
		case "tool":
			input = append(input, map[string]any{
				"call_id": message.ToolCallID,
				"output":  stringifyContent(message.Content),
				"type":    "function_call_output",
			})
		case "assistant":
			if text := stringifyContent(message.Content); text != "" {
				input = append(input, map[string]any{
					"content": []any{map[string]any{"text": text, "type": "output_text"}},
					"role":    "assistant",
					"type":    "message",
				})
			}
			for _, toolCall := range message.ToolCalls {
				if toolCall.ID == "" || toolCall.Function.Name == "" {
					continue
				}
				toolNamesByID[toolCall.ID] = toolCall.Function.Name
				input = append(input, map[string]any{
					"arguments": toolCall.Function.Arguments,
					"call_id":   toolCall.ID,
					"name":      toolCall.Function.Name,
					"type":      "function_call",
				})
			}
		default:
			role := message.Role
			if role == "" {
				role = "user"
			}
			input = append(input, map[string]any{
				"content": toInputContent(message.Content),
				"role":    role,
				"type":    "message",
			})
		}
	}
	_ = toolNamesByID

	return input
}

func (chat chatRequest) toResponsesTools() []any {
	tools := make([]any, 0, len(chat.Tools))
	for _, tool := range chat.Tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		parameters := tool.Function.Parameters
		if parameters == nil {
			parameters = map[string]any{
				"additionalProperties": true,
				"properties":           map[string]any{},
				"type":                 "object",
			}
		}
		tools = append(tools, map[string]any{
			"description": tool.Function.Description,
			"name":        tool.Function.Name,
			"parameters":  parameters,
			"type":        "function",
		})
	}

	return tools
}

func toInputContent(content any) []any {
	text := stringifyContent(content)
	if text == "" {
		return []any{}
	}

	return []any{map[string]any{"text": text, "type": "input_text"}}
}

func stringifyContent(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var builder strings.Builder
		for _, item := range value {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if itemMap["type"] == "text" {
				if text, ok := itemMap["text"].(string); ok {
					builder.WriteString(text)
				}
			}
		}
		return builder.String()
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(encoded)
	}
}

func toChatCompletion(response map[string]any, request chatRequest) map[string]any {
	model := request.Model
	if model == "" {
		model = defaultChatModel
	}

	toolCalls := extractToolCalls(response)
	message := map[string]any{
		"content": extractText(response),
		"role":    "assistant",
	}
	if len(toolCalls) > 0 {
		message["content"] = nil
		message["tool_calls"] = toolCalls
	}

	return map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": finishReason(response),
			"index":         0,
			"message":       message,
		}},
		"created": time.Now().Unix(),
		"id":      "chatcmpl_" + responseID(response),
		"model":   model,
		"object":  "chat.completion",
		"usage":   usage(response),
	}
}

func (handler *Handler) streamChatResponse(responseWriter http.ResponseWriter, upstream *http.Response, request chatRequest) {
	addHeaders(responseWriter.Header(), sseHeaders)
	addHeaders(responseWriter.Header(), corsHeaders)
	responseWriter.WriteHeader(http.StatusOK)

	id := "chatcmpl_" + randomishID()
	created := time.Now().Unix()
	model := request.Model
	if model == "" {
		model = defaultChatModel
	}

	writeChatSSE(responseWriter, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}, "finish_reason": nil, "index": 0}},
		"created": created,
		"id":      id,
		"model":   model,
		"object":  "chat.completion.chunk",
	})

	events, err := sse.ReadAll(upstream.Body)
	if err != nil {
		return
	}
	for _, event := range events {
		chunks := chatChunksFromEvent(event, id, created, model)
		for _, chunk := range chunks {
			writeChatSSE(responseWriter, chunk)
		}
	}

	if _, err := responseWriter.Write(sse.Done()); err != nil {
		return
	}
}

func chatChunksFromEvent(event sse.Event, id string, created int64, model string) []any {
	if event.Data == "" {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
		return nil
	}

	eventType, _ := payload["type"].(string)
	var chunks []any
	if delta, ok := payload["delta"].(string); ok && strings.Contains(eventType, "output_text.delta") {
		chunks = append(chunks, chatDeltaChunk(id, created, model, map[string]any{"content": delta}, nil))
	}
	if delta, ok := payload["delta"].(string); ok && strings.Contains(eventType, "function_call_arguments.delta") {
		chunks = append(chunks, chatDeltaChunk(id, created, model, map[string]any{"tool_calls": []any{map[string]any{
			"function": map[string]any{"arguments": delta},
			"index":    0,
		}}}, nil))
	}
	if response, ok := payload["response"].(map[string]any); ok {
		text := extractText(response)
		if text != "" {
			chunks = append(chunks, chatDeltaChunk(id, created, model, map[string]any{"content": text}, nil))
		}
		chunks = append(chunks, chatDeltaChunk(id, created, model, map[string]any{}, finishReason(response)))
	}

	return chunks
}

func chatDeltaChunk(id string, created int64, model string, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{"delta": delta, "finish_reason": finish, "index": 0}},
		"created": created,
		"id":      id,
		"model":   model,
		"object":  "chat.completion.chunk",
	}
}

func writeChatSSE(writer io.Writer, value any) {
	encoded, err := sse.EncodeData(value)
	if err != nil {
		return
	}
	_, _ = writer.Write(encoded)
}

func extractText(response map[string]any) string {
	output, ok := response["output"].([]any)
	if !ok {
		return ""
	}

	var builder strings.Builder
	for _, item := range output {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := itemMap["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range content {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := partMap["text"].(string); ok {
				builder.WriteString(text)
			}
		}
	}

	return builder.String()
}

func extractToolCalls(response map[string]any) []any {
	output, ok := response["output"].([]any)
	if !ok {
		return nil
	}

	toolCalls := make([]any, 0)
	for index, item := range output {
		itemMap, ok := item.(map[string]any)
		if !ok || itemMap["type"] != "function_call" {
			continue
		}
		toolCalls = append(toolCalls, map[string]any{
			"function": map[string]any{
				"arguments": itemMap["arguments"],
				"name":      itemMap["name"],
			},
			"id":    itemMap["call_id"],
			"index": index,
			"type":  "function",
		})
	}

	return toolCalls
}

func finishReason(response map[string]any) any {
	status, _ := response["status"].(string)
	switch status {
	case "completed":
		if len(extractToolCalls(response)) > 0 {
			return "tool_calls"
		}
		return "stop"
	case "incomplete":
		return "length"
	default:
		return nil
	}
}

func usage(response map[string]any) map[string]any {
	rawUsage, _ := response["usage"].(map[string]any)
	inputTokens := numberValue(rawUsage["input_tokens"])
	outputTokens := numberValue(rawUsage["output_tokens"])
	return map[string]any{
		"completion_tokens": outputTokens,
		"prompt_tokens":     inputTokens,
		"total_tokens":      inputTokens + outputTokens,
	}
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	default:
		return 0
	}
}

func responseID(response map[string]any) string {
	id, _ := response["id"].(string)
	if id == "" {
		return randomishID()
	}

	return id
}

func randomishID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}
