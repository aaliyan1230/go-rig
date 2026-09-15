package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodeToolArgs decodes one strict JSON object. Tool arguments come from a
// model, so accepting a typo or a second JSON value would be surprisingly
// difficult to diagnose later in the agent loop.
func decodeToolArgs(params json.RawMessage, toolName string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid %s arguments: %w", toolName, err)
	}
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("invalid %s arguments: multiple JSON values", toolName)
	}
	return fmt.Errorf("invalid %s arguments: %w", toolName, err)
}

func jsonFieldIsNull(params json.RawMessage, field string) bool {
	raw, ok := jsonField(params, field)
	return ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func jsonFieldPresent(params json.RawMessage, field string) bool {
	_, ok := jsonField(params, field)
	return ok
}

func jsonField(params json.RawMessage, field string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, false
	}
	raw, ok := fields[field]
	return raw, ok
}
