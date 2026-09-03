package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fingon/go-openai-proxy/internal/codex"
	"github.com/fingon/go-openai-proxy/internal/sse"
)

type chatRequest struct {
	Functions         []chatToolFunction `json:"functions,omitempty"`
	FunctionCall      any                `json:"function_call,omitempty"`
	MaxTokens         int                `json:"max_tokens,omitempty"`
	Messages          []chatMessage      `json:"messages"`
	Model             string             `json:"model,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string             `json:"reasoning_effort,omitempty"`
	ResponseFormat    any                `json:"response_format,omitempty"`
	ServiceTier       string             `json:"service_tier,omitempty"`
	Stop              any                `json:"stop,omitempty"`
	Stream            bool               `json:"stream,omitempty"`
	StreamOptions     *chatStreamOptions `json:"stream_options,omitempty"`
	Temperature       *float64           `json:"temperature,omitempty"`
	ToolChoice        any                `json:"tool_choice,omitempty"`
	Tools             []chatTool         `json:"tools,omitempty"`
	TopP              *float64           `json:"top_p,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type chatMessage struct {
	Content          any            `json:"content,omitempty"`
	Name             string         `json:"name,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Role             string         `json:"role,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
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

const (
	chatCompletionChunkObject = "chat.completion.chunk"
	chatRoleAssistant         = "assistant"
	chatToolTypeFunction      = "function"
	responsesInputTextType    = "input_text"
	responseFormatJSONObject  = "json_object"
	responseFormatJSONSchema  = "json_schema"
)

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
	if len(chat.Tools) > 0 || len(chat.Functions) > 0 {
		payload["tools"] = chat.toResponsesTools()
	}
	if chat.ToolChoice != nil {
		payload["tool_choice"] = chat.ToolChoice
	} else if converted := convertFunctionCallToToolChoice(chat.FunctionCall); converted != nil {
		payload["tool_choice"] = converted
	}
	if format := responseFormatToTextFormat(chat.ResponseFormat); format != nil {
		payload["text"] = format
	}
	if chat.ParallelToolCalls != nil {
		payload["parallel_tool_calls"] = *chat.ParallelToolCalls
	}
	if chat.ReasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": chat.ReasoningEffort}
	}
	if chat.ServiceTier != "" {
		payload["service_tier"] = chat.ServiceTier
	}

	return payload
}

// responseFormatToTextFormat converts a Chat Completions response_format into
// the Responses API text.format shape; unsupported values return nil.
func responseFormatToTextFormat(value any) any {
	format, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	switch format["type"] {
	case responseFormatJSONObject:
		return map[string]any{"format": map[string]any{"type": responseFormatJSONObject}}
	case responseFormatJSONSchema:
		schema := map[string]any{"type": responseFormatJSONSchema}
		jsonSchema, _ := format[responseFormatJSONSchema].(map[string]any)
		for _, key := range []string{"name", "schema", "strict", "description"} {
			if entry, ok := jsonSchema[key]; ok && entry != nil {
				schema[key] = entry
			}
		}
		return map[string]any{"format": schema}
	default:
		return nil
	}
}

// convertFunctionCallToToolChoice maps the legacy function_call field to a
// Responses tool_choice value; nil is returned when there is nothing to map.
func convertFunctionCallToToolChoice(value any) any {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return typed
	case map[string]any:
		name, _ := typed["name"].(string)
		if name == "" {
			return nil
		}
		return map[string]any{"name": name, "type": chatToolTypeFunction}
	default:
		return nil
	}
}

func (chat chatRequest) toResponsesInput() []any {
	input := make([]any, 0, len(chat.Messages))
	for _, message := range chat.Messages {
		switch message.Role {
		case "tool":
			input = append(input, map[string]any{
				"call_id": message.ToolCallID,
				"output":  stringifyContent(message.Content),
				"type":    "function_call_output",
			})
		case "function":
			// Legacy function result rows carry no call id; the function name
			// doubles as one so the output stays paired with its call.
			input = append(input, map[string]any{
				"call_id": message.Name,
				"output":  stringifyContent(message.Content),
				"type":    "function_call_output",
			})
		case chatRoleAssistant:
			if text := assistantText(message); text != "" {
				input = append(input, map[string]any{
					"content": []any{map[string]any{"text": text, "type": "output_text"}},
					"role":    chatRoleAssistant,
					"type":    "message",
				})
			}
			for _, toolCall := range message.ToolCalls {
				if toolCall.ID == "" || toolCall.Function.Name == "" {
					continue
				}
				input = append(input, map[string]any{
					"arguments": toolCall.Function.Arguments,
					"call_id":   toolCall.ID,
					"name":      toolCall.Function.Name,
					"type":      itemTypeFunctionCall,
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

	return input
}

// assistantText renders assistant history as text; reasoning content from
// compatible clients is preserved inside explicit thinking tags.
func assistantText(message chatMessage) string {
	text := stringifyContent(message.Content)
	if message.ReasoningContent != "" {
		reasoning := "<thinking>" + message.ReasoningContent + "</thinking>"
		if text != "" {
			reasoning += "\n" + text
		}
		text = reasoning
	}

	return text
}

func (chat chatRequest) toResponsesTools() []any {
	tools := make([]any, 0, len(chat.Tools)+len(chat.Functions))
	for _, tool := range chat.Tools {
		if tool.Type != chatToolTypeFunction || tool.Function.Name == "" {
			continue
		}
		tools = append(tools, map[string]any{
			"description": tool.Function.Description,
			"name":        tool.Function.Name,
			"parameters":  toolParameters(tool.Function),
			"type":        chatToolTypeFunction,
		})
	}
	// Legacy functions[] entries are plain function definitions.
	for _, function := range chat.Functions {
		if function.Name == "" {
			continue
		}
		tools = append(tools, map[string]any{
			"description": function.Description,
			"name":        function.Name,
			"parameters":  toolParameters(function),
			"type":        chatToolTypeFunction,
		})
	}

	return tools
}

func toolParameters(function chatToolFunction) map[string]any {
	parameters := function.Parameters
	if parameters == nil {
		parameters = map[string]any{
			"additionalProperties": true,
			"properties":           map[string]any{},
			"type":                 "object",
		}
	}

	return parameters
}

// toInputContent converts Chat Completions message content into Responses
// input content parts; text becomes input_text and image URLs become
// input_image parts.
func toInputContent(content any) []any {
	parts, ok := content.([]any)
	if !ok {
		text := stringifyContent(content)
		if text == "" {
			return []any{}
		}
		return []any{map[string]any{"text": text, "type": responsesInputTextType}}
	}

	converted := make([]any, 0, len(parts))
	for _, part := range parts {
		switch typedPart := part.(type) {
		case string:
			if typedPart != "" {
				converted = append(converted, map[string]any{"text": typedPart, "type": responsesInputTextType})
			}
		case map[string]any:
			switch partType, _ := typedPart["type"].(string); partType {
			case "", "text":
				if text, ok := typedPart["text"].(string); ok && text != "" {
					converted = append(converted, map[string]any{"text": text, "type": responsesInputTextType})
				}
			case "image_url":
				if imageURL := extractImageURL(typedPart["image_url"]); imageURL != "" {
					converted = append(converted, map[string]any{"image_url": imageURL, "type": "input_image"})
				}
			case "input_image":
				converted = append(converted, typedPart)
			case responsesInputTextType:
				converted = append(converted, typedPart)
			default:
				// Other modalities are not supported by this endpoint; their
				// text form is preserved so nothing disappears silently.
				if text := stringifyContent(typedPart); text != "" && text != "{}" {
					converted = append(converted, map[string]any{"text": text, "type": responsesInputTextType})
				}
			}
		}
	}

	return converted
}

func extractImageURL(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		url, _ := typed["url"].(string)
		return url
	default:
		return ""
	}
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
		"role":    chatRoleAssistant,
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
		"usage":   usageFromResponse(response),
	}
}

func (handler *Handler) streamChatResponse(responseWriter http.ResponseWriter, upstream *http.Response, request chatRequest) {
	addHeaders(responseWriter.Header(), sseHeaders)
	addHeaders(responseWriter.Header(), corsHeaders)
	responseWriter.WriteHeader(http.StatusOK)

	flusher, _ := responseWriter.(http.Flusher)

	id := "chatcmpl_" + randomishID()
	created := time.Now().Unix()
	model := request.Model
	if model == "" {
		model = defaultChatModel
	}

	writeChatSSE(responseWriter, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"role": chatRoleAssistant}, "finish_reason": nil, "index": 0}},
		"created": created,
		"id":      id,
		"model":   model,
		"object":  chatCompletionChunkObject,
	})
	if flusher != nil {
		flusher.Flush()
	}

	reader := sse.NewReader(upstream.Body)
	tracker := newStreamedToolCallTracker()
	var finalUsage any
	for {
		event, ok := reader.Next()
		if !ok {
			break
		}
		chunks, eventUsage := chatChunksFromEvent(event, id, created, model, tracker)
		for _, chunk := range chunks {
			writeChatSSE(responseWriter, chunk)
		}
		if len(chunks) > 0 && flusher != nil {
			flusher.Flush()
		}
		if eventUsage != nil {
			finalUsage = eventUsage
		}
	}
	if err := reader.Err(); err != nil {
		slog.Error("read upstream chat stream failed", "error", err)
	}

	if finalUsage != nil && request.StreamOptions != nil && request.StreamOptions.IncludeUsage {
		writeChatSSE(responseWriter, map[string]any{
			"choices": []any{},
			"created": created,
			"id":      id,
			"model":   model,
			"object":  chatCompletionChunkObject,
			"usage":   finalUsage,
		})
		if flusher != nil {
			flusher.Flush()
		}
	}

	if _, err := responseWriter.Write(sse.Done()); err != nil {
		slog.Error("write chat stream done failed", "error", err)
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}

// streamedToolCall tracks one upstream function_call item as it streams so its
// deltas can be mapped to a Chat Completions tool_calls index.
type streamedToolCall struct {
	chatIndex  int
	deltasSent bool
}

type streamedToolCallTracker struct {
	byItemID         map[string]*streamedToolCall
	byOutputIndex    map[int]*streamedToolCall
	finishReasonSent bool
	hasTools         bool
	nextChatIndex    int
}

func newStreamedToolCallTracker() *streamedToolCallTracker {
	return &streamedToolCallTracker{
		byItemID:      map[string]*streamedToolCall{},
		byOutputIndex: map[int]*streamedToolCall{},
	}
}

func (tracker *streamedToolCallTracker) register(outputIndex int, itemID string) *streamedToolCall {
	call := &streamedToolCall{chatIndex: tracker.nextChatIndex}
	tracker.nextChatIndex++
	tracker.hasTools = true
	if outputIndex >= 0 {
		tracker.byOutputIndex[outputIndex] = call
	}
	if itemID != "" {
		tracker.byItemID[itemID] = call
	}

	return call
}

func (tracker *streamedToolCallTracker) lookup(outputIndex int, itemID string) *streamedToolCall {
	if outputIndex >= 0 {
		if call, ok := tracker.byOutputIndex[outputIndex]; ok {
			return call
		}
	}
	if itemID != "" {
		if call, ok := tracker.byItemID[itemID]; ok {
			return call
		}
	}

	return nil
}

func chatChunksFromEvent(event sse.Event, id string, created int64, model string, tracker *streamedToolCallTracker) ([]any, any) {
	if event.Data == "" {
		return nil, nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
		return nil, nil
	}

	eventType := eventTypeOf(event, payload)
	var usage any
	var chunks []any

	switch {
	case strings.Contains(eventType, "output_text.delta"):
		if delta, ok := payload["delta"].(string); ok {
			chunks = append(chunks, chatDeltaChunk(id, created, model, map[string]any{"content": delta}, nil))
		}
	case strings.Contains(eventType, "function_call_arguments.delta"):
		if delta, ok := payload["delta"].(string); ok {
			chunks = append(chunks, tracker.argumentsDeltaChunk(payload, delta, id, created, model)...)
		}
	case strings.HasSuffix(eventType, "output_item.added"):
		chunks = append(chunks, tracker.itemAddedChunks(payload, id, created, model)...)
	case strings.HasSuffix(eventType, "output_item.done"):
		chunks = append(chunks, tracker.itemDoneChunks(payload, id, created, model)...)
	case strings.Contains(eventType, "error"):
		slog.Warn("upstream chat stream error event", "event", eventType)
	}

	if response, ok := payload["response"].(map[string]any); ok {
		if _, ok := response["usage"].(map[string]any); ok {
			usage = usageFromResponse(response)
		}
		if !tracker.finishReasonSent {
			if finish := streamingFinishReason(response, tracker); finish != nil {
				tracker.finishReasonSent = true
				chunks = append(chunks, chatDeltaChunk(id, created, model, map[string]any{}, finish))
			}
		}
	}

	return chunks, usage
}

// eventTypeOf prefers the typed field inside the data payload and falls back
// to the SSE event name.
func eventTypeOf(event sse.Event, payload map[string]any) string {
	if eventType, ok := payload["type"].(string); ok && eventType != "" {
		return eventType
	}

	return event.Event
}

func (tracker *streamedToolCallTracker) argumentsDeltaChunk(payload map[string]any, delta, id string, created int64, model string) []any {
	call := tracker.lookup(intValueFrom(payload["output_index"]), stringValueFrom(payload["item_id"]))
	if call == nil {
		call = tracker.register(-1, stringValueFrom(payload["item_id"]))
	}
	call.deltasSent = true

	return []any{chatDeltaChunk(id, created, model, map[string]any{
		"tool_calls": []any{map[string]any{
			chatToolTypeFunction: map[string]any{"arguments": delta},
			"index":              call.chatIndex,
		}},
	}, nil)}
}

func (tracker *streamedToolCallTracker) itemAddedChunks(payload map[string]any, id string, created int64, model string) []any {
	item, ok := payload["item"].(map[string]any)
	if !ok || item["type"] != itemTypeFunctionCall {
		return nil
	}

	outputIndex := intValueFrom(payload["output_index"])
	itemID := stringValueFrom(item["id"])
	call := tracker.lookup(outputIndex, itemID)
	if call == nil {
		call = tracker.register(outputIndex, itemID)
	}
	name := stringValueFrom(item["name"])

	return []any{chatDeltaChunk(id, created, model, map[string]any{
		"tool_calls": []any{map[string]any{
			chatToolTypeFunction: map[string]any{"name": name},
			"id":                 stringValueFrom(item["call_id"]),
			"index":              call.chatIndex,
			"type":               chatToolTypeFunction,
		}},
	}, nil)}
}

func (tracker *streamedToolCallTracker) itemDoneChunks(payload map[string]any, id string, created int64, model string) []any {
	item, ok := payload["item"].(map[string]any)
	if !ok || item["type"] != itemTypeFunctionCall {
		return nil
	}

	outputIndex := intValueFrom(payload["output_index"])
	itemID := stringValueFrom(item["id"])
	call := tracker.lookup(outputIndex, itemID)
	if call == nil {
		call = tracker.register(outputIndex, itemID)
	}
	if call.deltasSent {
		// Deltas already carried the full argument stream.
		return nil
	}
	call.deltasSent = true
	arguments := stringValueFrom(item["arguments"])

	return []any{chatDeltaChunk(id, created, model, map[string]any{
		"tool_calls": []any{map[string]any{
			chatToolTypeFunction: map[string]any{"arguments": arguments},
			"id":                 stringValueFrom(item["call_id"]),
			"index":              call.chatIndex,
			"type":               chatToolTypeFunction,
		}},
	}, nil)}
}

func intValueFrom(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return -1
	}
}

func stringValueFrom(value any) string {
	text, _ := value.(string)
	return text
}

func chatDeltaChunk(id string, created int64, model string, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{"delta": delta, "finish_reason": finish, "index": 0}},
		"created": created,
		"id":      id,
		"model":   model,
		"object":  chatCompletionChunkObject,
	}
}

func writeChatSSE(writer io.Writer, value any) {
	encoded, err := sse.EncodeData(value)
	if err != nil {
		slog.Error("encode chat SSE failed", "error", err)
		return
	}
	if _, err := writer.Write(encoded); err != nil {
		slog.Error("write chat SSE failed", "error", err)
	}
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
		if !ok || itemMap["type"] != itemTypeFunctionCall {
			continue
		}
		toolCalls = append(toolCalls, map[string]any{
			chatToolTypeFunction: map[string]any{
				"arguments": itemMap["arguments"],
				"name":      itemMap["name"],
			},
			"id":    itemMap["call_id"],
			"index": index,
			"type":  chatToolTypeFunction,
		})
	}

	return toolCalls
}

const (
	finishReasonLength    = "length"
	finishReasonStop      = "stop"
	finishReasonToolCalls = "tool_calls"
	itemTypeFunctionCall  = "function_call"
)

// streamingFinishReason maps a terminal Responses status to a Chat Completions
// finish reason, preferring streamed tool-call state over the (often empty)
// final response output array.
func streamingFinishReason(response map[string]any, tracker *streamedToolCallTracker) any {
	status, _ := response["status"].(string)
	switch status {
	case "completed":
		if tracker.hasTools {
			return finishReasonToolCalls
		}
		return finishReasonStop
	case "incomplete":
		return finishReasonLength
	default:
		return nil
	}
}

func finishReason(response map[string]any) any {
	tracker := &streamedToolCallTracker{hasTools: len(extractToolCalls(response)) > 0}
	return streamingFinishReason(response, tracker)
}

func usageFromResponse(response map[string]any) map[string]any {
	rawUsage, _ := response["usage"].(map[string]any)
	inputTokens := numberValue(rawUsage["input_tokens"])
	outputTokens := numberValue(rawUsage["output_tokens"])
	usage := map[string]any{
		"completion_tokens": outputTokens,
		"prompt_tokens":     inputTokens,
		"total_tokens":      inputTokens + outputTokens,
	}
	if details, ok := rawUsage["input_tokens_details"].(map[string]any); ok {
		usage["prompt_tokens_details"] = details
	}
	if details, ok := rawUsage["output_tokens_details"].(map[string]any); ok {
		usage["completion_tokens_details"] = details
	}
	return usage
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
