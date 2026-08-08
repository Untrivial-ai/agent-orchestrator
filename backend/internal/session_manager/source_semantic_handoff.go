package sessionmanager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	sourceSemanticHandoffSchemaVersion = 1
	sourceSemanticHandoffMaxBytes      = 64 << 10
	sourceSemanticHandoffMaxFields     = 128
	sourceSemanticHandoffMaxKeyBytes   = 256
	sourceSemanticHandoffMaxTextBytes  = 16 << 10
	sourceSemanticHandoffMaxListItems  = 256
	sourceSemanticHandoffMaxItemBytes  = 8 << 10
)

var sourceSemanticHandoffKnownFields = map[string]semanticHandoffFieldKind{
	"schemaVersion":                 semanticHandoffVersionField,
	"goal":                          semanticHandoffTextField,
	"latestUserIntent":              semanticHandoffTextField,
	"progressSummary":               semanticHandoffTextField,
	"completedWork":                 semanticHandoffTextListField,
	"currentWork":                   semanticHandoffTextField,
	"decisions":                     semanticHandoffTextListField,
	"rejectedApproaches":            semanticHandoffTextListField,
	"relevantFiles":                 semanticHandoffTextListField,
	"testsAndResults":               semanticHandoffTextListField,
	"blockers":                      semanticHandoffTextListField,
	"uncertainties":                 semanticHandoffTextListField,
	"risks":                         semanticHandoffTextListField,
	"recommendedNextSteps":          semanticHandoffTextListField,
	"userPreferencesAndConstraints": semanticHandoffTextListField,
	"freeformDetails":               semanticHandoffTextField,
	"taskComplete":                  semanticHandoffBoolField,
}

type semanticHandoffFieldKind uint8

const (
	semanticHandoffVersionField semanticHandoffFieldKind = iota
	semanticHandoffTextField
	semanticHandoffTextListField
	semanticHandoffBoolField
)

// validateAndCanonicalizeSourceSemanticHandoff validates one V1 semantic
// handoff and returns deterministic JSON with every unknown top-level field
// preserved. It intentionally does not interpret unknown values, but they still
// count toward the global size and field-count bounds.
func validateAndCanonicalizeSourceSemanticHandoff(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("source semantic handoff must be one JSON object")
	}
	if len(raw) > sourceSemanticHandoffMaxBytes {
		return nil, fmt.Errorf("source semantic handoff exceeds %d bytes", sourceSemanticHandoffMaxBytes)
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, errors.New("source semantic handoff must be one valid JSON object")
	}

	fields, err := decodeSemanticHandoffObject(trimmed)
	if err != nil {
		return nil, err
	}
	if len(fields) > sourceSemanticHandoffMaxFields {
		return nil, fmt.Errorf("source semantic handoff has %d fields; maximum is %d", len(fields), sourceSemanticHandoffMaxFields)
	}

	for name, value := range fields {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("source semantic handoff field names must not be empty")
		}
		if len(name) > sourceSemanticHandoffMaxKeyBytes {
			return nil, fmt.Errorf("source semantic handoff field name exceeds %d bytes", sourceSemanticHandoffMaxKeyBytes)
		}
		kind, known := sourceSemanticHandoffKnownFields[name]
		if !known {
			continue
		}
		if err := validateSemanticHandoffField(name, value, kind); err != nil {
			return nil, err
		}
	}

	for _, required := range []string{"schemaVersion", "goal", "progressSummary"} {
		if _, ok := fields[required]; !ok {
			return nil, fmt.Errorf("source semantic handoff field %q is required", required)
		}
	}

	canonical, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("canonicalize source semantic handoff: %w", err)
	}
	if len(canonical) > sourceSemanticHandoffMaxBytes {
		return nil, fmt.Errorf("canonical source semantic handoff exceeds %d bytes", sourceSemanticHandoffMaxBytes)
	}
	return json.RawMessage(canonical), nil
}

// decodeSemanticHandoffObject rejects duplicate top-level keys rather than
// allowing encoding/json's usual last-value-wins behavior to silently discard
// a source-provided value.
func decodeSemanticHandoffObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return nil, errors.New("source semantic handoff must be one valid JSON object")
	}
	delim, ok := start.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("source semantic handoff must be one valid JSON object")
	}

	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, errors.New("source semantic handoff contains an invalid field name")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("source semantic handoff contains an invalid field name")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("source semantic handoff contains duplicate field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("source semantic handoff field %q is invalid: %w", key, err)
		}
		fields[key] = append(json.RawMessage(nil), value...)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("source semantic handoff must be one valid JSON object")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, errors.New("source semantic handoff must contain exactly one JSON object")
	}
	return fields, nil
}

func validateSemanticHandoffField(name string, raw json.RawMessage, kind semanticHandoffFieldKind) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("source semantic handoff field %q must not be null", name)
	}

	switch kind {
	case semanticHandoffVersionField:
		var version int
		if err := json.Unmarshal(raw, &version); err != nil {
			return fmt.Errorf("source semantic handoff field %q must be integer %d", name, sourceSemanticHandoffSchemaVersion)
		}
		if version != sourceSemanticHandoffSchemaVersion {
			return fmt.Errorf("source semantic handoff schemaVersion must be %d", sourceSemanticHandoffSchemaVersion)
		}
		return nil
	case semanticHandoffTextField:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("source semantic handoff field %q must be a string", name)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("source semantic handoff field %q must not be empty", name)
		}
		if len(value) > sourceSemanticHandoffMaxTextBytes {
			return fmt.Errorf("source semantic handoff field %q exceeds %d bytes", name, sourceSemanticHandoffMaxTextBytes)
		}
		return nil
	case semanticHandoffTextListField:
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("source semantic handoff field %q must be an array of strings", name)
		}
		if len(values) > sourceSemanticHandoffMaxListItems {
			return fmt.Errorf("source semantic handoff field %q has %d items; maximum is %d", name, len(values), sourceSemanticHandoffMaxListItems)
		}
		for i, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("source semantic handoff field %q item %d must not be empty", name, i)
			}
			if len(value) > sourceSemanticHandoffMaxItemBytes {
				return fmt.Errorf("source semantic handoff field %q item %d exceeds %d bytes", name, i, sourceSemanticHandoffMaxItemBytes)
			}
		}
		return nil
	case semanticHandoffBoolField:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("source semantic handoff field %q must be a boolean", name)
		}
		return nil
	default:
		return fmt.Errorf("source semantic handoff field %q has an unknown validator", name)
	}
}
