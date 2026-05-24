package sse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Event struct {
	Data  string
	Event string
}

func ReadAll(reader io.Reader) ([]Event, error) {
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}

	blocks := bytes.Split(content, []byte("\n\n"))
	events := make([]Event, 0, len(blocks))
	for _, block := range blocks {
		if len(bytes.TrimSpace(block)) == 0 {
			continue
		}
		events = append(events, parseBlock(block))
	}

	return events, nil
}

func CollectCompletedResponse(reader io.Reader) (map[string]any, error) {
	events, err := ReadAll(reader)
	if err != nil {
		return nil, err
	}

	var latestResponse map[string]any
	var latestError any
	for _, event := range events {
		if event.Data == "" {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			continue
		}
		if event.Event == "error" {
			latestError = payload
			continue
		}

		response, ok := payload["response"].(map[string]any)
		if ok {
			latestResponse = response
		}
	}

	if latestResponse != nil {
		return latestResponse, nil
	}
	if latestError != nil {
		return nil, fmt.Errorf("no completed response found in SSE stream; last error: %v", latestError)
	}

	return nil, errors.New("no completed response found in SSE stream")
}

func EncodeData(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal SSE data: %w", err)
	}

	return []byte("data: " + string(encoded) + "\n\n"), nil
}

func Done() []byte {
	return []byte("data: [DONE]\n\n")
}

func parseBlock(block []byte) Event {
	var event Event
	var dataLines []string
	scanner := bufio.NewScanner(bytes.NewReader(block))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	event.Data = strings.Join(dataLines, "\n")

	return event
}
