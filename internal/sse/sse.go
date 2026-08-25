package sse

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Event struct {
	Data  string
	Event string
}

// Reader reads SSE events incrementally as they arrive on the wire instead of
// buffering the whole stream.
type Reader struct {
	scanner *bufio.Scanner
}

func NewReader(reader io.Reader) *Reader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventSizeBytes)

	return &Reader{scanner: scanner}
}

const maxEventSizeBytes = 32 << 20

// Next returns the next event; ok is false at end of stream.
func (reader *Reader) Next() (Event, bool) {
	var (
		dataLines []string
		eventName string
		started   bool
	)
	for reader.scanner.Scan() {
		line := strings.TrimRight(reader.scanner.Text(), "\r")
		if line == "" {
			if !started {
				continue
			}
			return Event{Data: strings.Join(dataLines, "\n"), Event: eventName}, true
		}
		started = true
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if started {
		return Event{Data: strings.Join(dataLines, "\n"), Event: eventName}, true
	}

	return Event{}, false
}

// Err reports the first read or token-size error encountered while scanning.
func (reader *Reader) Err() error {
	return reader.scanner.Err()
}

func ReadAll(reader io.Reader) ([]Event, error) {
	events := make([]Event, 0, 64)
	sseReader := NewReader(reader)
	for {
		event, ok := sseReader.Next()
		if !ok {
			break
		}
		if event.Data == "" && event.Event == "" {
			continue
		}
		events = append(events, event)
	}
	if err := sseReader.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
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
	outputItems := make(map[int]any)
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

		if item, ok := payload["item"]; ok && event.Event == "response.output_item.done" {
			if outputIndex, ok := intValue(payload["output_index"]); ok {
				outputItems[outputIndex] = item
			}
		}

		response, ok := payload["response"].(map[string]any)
		if ok {
			latestResponse = response
		}
	}

	if latestResponse != nil {
		if len(outputItems) > 0 && len(outputArray(latestResponse["output"])) == 0 {
			latestResponse["output"] = orderedValues(outputItems)
		}
		return latestResponse, nil
	}
	if latestError != nil {
		return nil, fmt.Errorf("no completed response found in SSE stream; last error: %v", latestError)
	}

	return nil, errors.New("no completed response found in SSE stream")
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func outputArray(value any) []any {
	output, ok := value.([]any)
	if !ok {
		return nil
	}

	return output
}

func orderedValues(values map[int]any) []any {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)

	ordered := make([]any, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, values[key])
	}

	return ordered
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
