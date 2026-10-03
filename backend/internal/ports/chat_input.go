package ports

import (
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// ValidateChatInputResponse validates the response against the provider's offered schema.
func ValidateChatInputResponse(request ChatInputRequest, response ChatInputResponse) error {
	switch response.Action {
	case ChatInputActionDecline, ChatInputActionCancel:
		return nil
	case ChatInputActionAccept:
		if request.Mode == ChatInputModeURL {
			return nil
		}
	default:
		return fmt.Errorf("%w: unsupported input action %q", ErrChatDecisionNotOffered, response.Action)
	}
	if request.Mode != ChatInputModeForm {
		return fmt.Errorf("%w: unsupported input mode %q", ErrChatDecisionNotOffered, request.Mode)
	}
	if err := validateFormContent(request.Schema, response.Content); err != nil {
		return fmt.Errorf("%w: %s", ErrChatDecisionNotOffered, err.Error())
	}
	return nil
}

func validateFormContent(schema, content map[string]any) error {
	for _, name := range formRequired(schema["required"]) {
		if name == "" {
			continue
		}
		if _, present := content[name]; !present {
			return fmt.Errorf("required input %q is missing", name)
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, value := range content {
		rawProperty, known := properties[name]
		if !known {
			return fmt.Errorf("input %q is not in the requested schema", name)
		}
		property, ok := rawProperty.(map[string]any)
		if !ok {
			return fmt.Errorf("input %q has an invalid requested schema", name)
		}
		if err := validateFormValue(value, property); err != nil {
			return fmt.Errorf("input %q %s", name, err.Error())
		}
	}
	return nil
}

func validateFormValue(value any, property map[string]any) error {
	typeName, _ := property["type"].(string)
	switch typeName {
	case "string", "":
		text, ok := value.(string)
		if !ok {
			return errors.New("must be a string")
		}
		if minimum, ok := number(property["minLength"]); ok && float64(utf8.RuneCountInString(text)) < minimum {
			return errors.New("is shorter than the minimum length")
		}
		if maximum, ok := number(property["maxLength"]); ok && float64(utf8.RuneCountInString(text)) > maximum {
			return errors.New("exceeds the maximum length")
		}
		if !formOptionOffered(text, property) {
			return errors.New("is not one of the offered values")
		}
	case "number", "integer":
		numeric, ok := number(value)
		if !ok || math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return errors.New("must be a finite number")
		}
		if minimum, ok := number(property["minimum"]); ok && numeric < minimum {
			return errors.New("is below the minimum")
		}
		if maximum, ok := number(property["maximum"]); ok && numeric > maximum {
			return errors.New("exceeds the maximum")
		}
		if typeName == "integer" && math.Trunc(numeric) != numeric {
			return errors.New("must be an integer")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("must be a boolean")
		}
	case "array":
		if strings, ok := value.([]string); ok {
			values := make([]any, len(strings))
			for i, v := range strings {
				values[i] = v
			}
			value = values
		}
		values, ok := value.([]any)
		if !ok {
			return errors.New("must be an array")
		}
		if minimum, ok := number(property["minItems"]); ok && float64(len(values)) < minimum {
			return errors.New("has too few selections")
		}
		if maximum, ok := number(property["maxItems"]); ok && float64(len(values)) > maximum {
			return errors.New("has too many selections")
		}
		items, _ := property["items"].(map[string]any)
		for _, item := range values {
			text, ok := item.(string)
			if !ok || !formOptionOffered(text, items) {
				return errors.New("contains a value that was not offered")
			}
		}
	default:
		return fmt.Errorf("uses unsupported type %q", typeName)
	}
	return nil
}

func formRequired(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func formOptionOffered(value string, schema map[string]any) bool {
	var options []any
	if candidates, ok := schema["oneOf"].([]any); ok {
		options = candidates
	} else if candidates, ok := schema["anyOf"].([]any); ok {
		options = candidates
	} else if candidates, ok := schema["enum"].([]any); ok {
		for _, candidate := range candidates {
			if candidate == value {
				return true
			}
		}
		return len(candidates) == 0
	}
	if len(options) == 0 {
		return true
	}
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if option["const"] == value {
			return true
		}
	}
	return false
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
