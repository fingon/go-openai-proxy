package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestChatCompletionsPromotesSystemMessages(t *testing.T) {
	var upstreamBody string
	transport := &recordingTransport{}
	transport.handler = func(_ *http.Request, body string) (*http.Response, error) {
		upstreamBody = body
		return textResponse(http.StatusOK, strings.Join([]string{
			"event: response.output_item.done",
			`data: {"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"ok"}],"role":"assistant"}}`,
			"",
			"event: response.completed",
			`data: {"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON(`/v1/chat/completions`, `{"model":"gpt-5.2","messages":[{"role":"system","content":"Be terse."},{"role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	assert.Assert(t, !strings.Contains(upstreamBody, `"role":"system"`), upstreamBody)
	assert.Assert(t, strings.Contains(upstreamBody, `"instructions"`), upstreamBody)

	var payload map[string]any
	assert.NilError(t, json.Unmarshal([]byte(upstreamBody), &payload))
	instructions, _ := payload["instructions"].(string)
	assert.Assert(t, strings.Contains(instructions, "Be terse."), instructions)
	input, _ := payload["input"].([]any)
	assert.Equal(t, len(input), 1)
}

func TestChatCompletionsConvertsJSONSchemaResponseFormat(t *testing.T) {
	var upstreamBody string
	transport := &recordingTransport{}
	transport.handler = func(_ *http.Request, body string) (*http.Response, error) {
		upstreamBody = body
		return textResponse(http.StatusOK, strings.Join([]string{
			"event: response.completed",
			`data: {"response":{"id":"resp_1","status":"completed","output":[]}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON("/v1/chat/completions", `{
		"model":"gpt-5.2",
		"messages":[{"role":"user","content":"name this session"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"session_title",
				"schema":{
					"type":"object",
					"properties":{"title":{"type":"string"}},
					"required":["title"],
					"additionalProperties":false
				},
				"strict":true
			}
		}
	}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	var payload map[string]any
	assert.NilError(t, json.Unmarshal([]byte(upstreamBody), &payload))
	textFormat := payload["text"].(map[string]any)["format"].(map[string]any)
	assert.Equal(t, textFormat["type"], responseFormatJSONSchema)
	assert.Equal(t, textFormat["name"], "session_title")
	assert.Equal(t, textFormat["strict"], true)
	schema := textFormat["schema"].(map[string]any)
	assert.Equal(t, schema["type"], "object")
	assert.DeepEqual(t, schema["required"], []any{"title"})
	assert.Equal(t, schema["additionalProperties"], false)
	properties := schema["properties"].(map[string]any)
	assert.DeepEqual(t, properties["title"], map[string]any{"type": "string"})
	_, wrapped := textFormat[responseFormatJSONSchema]
	assert.Assert(t, !wrapped)
}

func TestResponsesEndpointPromotesSystemMessages(t *testing.T) {
	var upstreamBody string
	transport := &recordingTransport{}
	transport.handler = func(request *http.Request, body string) (*http.Response, error) {
		assert.Equal(t, request.URL.Path, "/backend-api/codex/responses")
		upstreamBody = body
		return textResponse(http.StatusOK, strings.Join([]string{
			"event: response.completed",
			`data: {"response":{"id":"resp_1","status":"completed","output":[]}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON("/v1/responses", `{"model":"gpt-5.2","input":[{"type":"message","role":"system","content":"You are terse."},{"type":"message","role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	assert.Assert(t, !strings.Contains(upstreamBody, `"role":"system"`), upstreamBody)
	assert.Assert(t, strings.Contains(upstreamBody, "You are terse."), upstreamBody)
}

func TestChatStreamingParallelToolCallsAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_a","call_id":"fc_calla","type":"function_call","name":"lookup","arguments":""}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_b","call_id":"fc_callb","type":"function_call","name":"fetch","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_a","output_index":0,"delta":"{\"q\""}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_b","output_index":1,"delta":"{}"}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_a","output_index":0,"delta":":1}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":5},"output_tokens_details":{"reasoning_tokens":2}}}}`,
		"",
	}, "\n")
	transport := &recordingTransport{}
	transport.handler = func(_ *http.Request, _ string) (*http.Response, error) {
		return textResponse(http.StatusOK, stream), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON("/v1/chat/completions", `{"model":"gpt-5.2","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"use tools"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	chunks := decodeChatChunks(t, response.Body.String())

	type callState struct {
		id      bool
		name    string
		args    string
		indices map[int]bool
	}
	states := map[int]*callState{}
	stateFor := func(index int) *callState {
		state, ok := states[index]
		if !ok {
			state = &callState{indices: map[int]bool{}}
			states[index] = state
		}
		return state
	}
	finishReasons := []string{}
	usageSeen := false
	for _, chunk := range chunks {
		usage, ok := chunk["usage"].(map[string]any)
		if ok {
			usageSeen = true
			assert.Assert(t, len(chunk["choices"].([]any)) == 0)
			assert.Equal(t, usage["total_tokens"], float64(10))
			promptDetails, ok := usage["prompt_tokens_details"].(map[string]any)
			assert.Assert(t, ok, "streamed usage must carry prompt_tokens_details")
			assert.Equal(t, promptDetails["cached_tokens"], float64(5))
			completionDetails, ok := usage["completion_tokens_details"].(map[string]any)
			assert.Assert(t, ok, "streamed usage must carry completion_tokens_details")
			assert.Equal(t, completionDetails["reasoning_tokens"], float64(2))
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice := choices[0].(map[string]any)
		if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
			finishReasons = append(finishReasons, finish)
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		rawToolCalls, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, rawToolCall := range rawToolCalls {
			toolCall := rawToolCall.(map[string]any)
			index := int(toolCall["index"].(float64))
			state := stateFor(index)
			state.indices[index] = true
			function := toolCall["function"].(map[string]any)
			if id, ok := toolCall["id"].(string); ok && id != "" {
				state.id = true
			}
			if name, ok := function["name"].(string); ok && name != "" {
				state.name = name
			}
			if arguments, ok := function["arguments"].(string); ok {
				state.args += arguments
			}
		}
	}

	assert.Equal(t, len(states), 2)
	assert.Equal(t, states[0].name, "lookup")
	assert.Equal(t, states[0].args, `{"q":1}`)
	assert.Assert(t, states[0].id, "first delta must carry the call id")
	assert.Equal(t, states[1].name, "fetch")
	assert.Equal(t, states[1].args, `{}`)
	assert.Equal(t, len(states[0].indices), 1)
	assert.Equal(t, len(states[1].indices), 1)
	assert.DeepEqual(t, finishReasons, []string{"tool_calls"})
	assert.Assert(t, usageSeen)
}

func TestChatCompletionsUsageDetailsPassthrough(t *testing.T) {
	transport := &recordingTransport{}
	transport.handler = func(_ *http.Request, _ string) (*http.Response, error) {
		return textResponse(http.StatusOK, strings.Join([]string{
			"event: response.completed",
			`data: {"response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":9,"output_tokens":4,"input_tokens_details":{"cached_tokens":6},"output_tokens_details":{"reasoning_tokens":3}}}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON("/v1/chat/completions", `{"model":"gpt-5.2","messages":[{"role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	var payload map[string]any
	assert.NilError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	usage, ok := payload["usage"].(map[string]any)
	assert.Assert(t, ok, payload["usage"])
	assert.Equal(t, usage["prompt_tokens"], float64(9))
	assert.Equal(t, usage["completion_tokens"], float64(4))
	promptDetails, ok := usage["prompt_tokens_details"].(map[string]any)
	assert.Assert(t, ok, "usage must carry prompt_tokens_details")
	assert.Equal(t, promptDetails["cached_tokens"], float64(6))
	completionDetails, ok := usage["completion_tokens_details"].(map[string]any)
	assert.Assert(t, ok, "usage must carry completion_tokens_details")
	assert.Equal(t, completionDetails["reasoning_tokens"], float64(3))
}

func TestChatStreamingTextDeltasIncrementally(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed","output":[]}}`,
		"",
	}, "\n")
	transport := &recordingTransport{}
	transport.handler = func(_ *http.Request, _ string) (*http.Response, error) {
		return textResponse(http.StatusOK, stream), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON("/v1/chat/completions", `{"model":"gpt-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	chunks := decodeChatChunks(t, response.Body.String())
	var text string
	stopSent := false
	var textSb200 strings.Builder
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice := choices[0].(map[string]any)
		if finish, ok := choice["finish_reason"].(string); ok && finish == "stop" {
			stopSent = true
			continue
		}
		delta := choice["delta"].(map[string]any)
		if content, ok := delta["content"].(string); ok {
			textSb200.WriteString(content)
		}
	}
	text += textSb200.String()
	assert.Equal(t, text, "hello")
	assert.Assert(t, stopSent)
}

func TestLegacyFunctionsAreConverted(t *testing.T) {
	var upstreamBody string
	transport := &recordingTransport{}
	transport.handler = func(_ *http.Request, body string) (*http.Response, error) {
		upstreamBody = body
		return textResponse(http.StatusOK, strings.Join([]string{
			"event: response.completed",
			`data: {"response":{"id":"resp_1","status":"completed","output":[]}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptestPostJSON("/v1/chat/completions", `{
		"model":"gpt-5.2",
		"functions":[{"name":"lookup","description":"find things","parameters":{"type":"object"}}],
		"function_call":{"name":"lookup"},
		"messages":[
			{"role":"user","content":"go"},
			{"role":"function","name":"lookup","content":"result-42"}
		]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(upstreamBody, `"name":"lookup"`), upstreamBody)
	assert.Assert(t, strings.Contains(upstreamBody, `"function_call_output"`), upstreamBody)
	assert.Assert(t, !strings.Contains(upstreamBody, `"functions"`), upstreamBody)

	var payload map[string]any
	assert.NilError(t, json.Unmarshal([]byte(upstreamBody), &payload))
	toolChoice, ok := payload["tool_choice"].(map[string]any)
	assert.Assert(t, ok, payload["tool_choice"])
	assert.Equal(t, toolChoice["type"], "function")
	assert.Equal(t, toolChoice["name"], "lookup")
}

func httptestPostJSON(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func decodeChatChunks(t *testing.T, body string) []map[string]any {
	t.Helper()

	var chunks []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		assert.NilError(t, json.Unmarshal([]byte(data), &chunk))
		chunks = append(chunks, chunk)
	}

	return chunks
}
