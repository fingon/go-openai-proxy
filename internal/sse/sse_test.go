package sse

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestReaderNext(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.created",
		`data: {"id":"1"}`,
		"",
		"",
		"data: line-one",
		"data: line-two",
		"",
	}, "\n")

	reader := NewReader(strings.NewReader(stream))

	first, ok := reader.Next()
	assert.Assert(t, ok)
	assert.Equal(t, first.Event, "response.created")
	assert.Equal(t, first.Data, `{"id":"1"}`)

	second, ok := reader.Next()
	assert.Assert(t, ok)
	assert.Equal(t, second.Event, "")
	assert.Equal(t, second.Data, "line-one\nline-two")

	_, ok = reader.Next()
	assert.Assert(t, !ok)
}

func TestReadAllPreservesOrderAndContent(t *testing.T) {
	stream := strings.Join([]string{
		"event: a",
		`data: {"n":1}`,
		"",
		"event: b",
		`data: {"n":2}`,
		"",
	}, "\n")

	events, err := ReadAll(strings.NewReader(stream))
	assert.NilError(t, err)
	assert.Equal(t, len(events), 2)
	assert.Equal(t, events[0].Event, "a")
	assert.Equal(t, events[1].Event, "b")
}

func TestEncodeDataAndDone(t *testing.T) {
	encoded, err := EncodeData(map[string]any{"k": "v"})
	assert.NilError(t, err)
	assert.Equal(t, string(encoded), "data: {\"k\":\"v\"}\n\n")
	assert.Equal(t, string(Done()), "data: [DONE]\n\n")
}
