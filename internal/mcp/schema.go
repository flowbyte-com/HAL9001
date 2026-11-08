package mcp

import (
	"encoding/json"
	"strings"
)

// A server's input schema is untrusted data that becomes part of the
// provider request. HAL keeps the validation structure — types,
// properties, required lists, enums and the other JSON Schema keywords —
// but removes annotation-only text that could carry instructions, and it
// refuses a schema that is malformed, too deep, too large or contains
// suspicious strings. A refused schema becomes the permissive fallback:
// the tool remains callable, and nothing in the schema becomes authority.
//
// The fallback deliberately accepts any arguments. A malformed schema
// must not silently narrow what the tool can do; the server remains
// responsible for validating its own input.

const (
	// maxSchemaDepth bounds the nesting of a schema.
	maxSchemaDepth = 32
	// maxSchemaNodes bounds the number of JSON values in a schema.
	maxSchemaNodes = 20000
	// maxSchemaString bounds one semantic string in a schema.
	maxSchemaString = 4 << 10
	// maxSchemaKey bounds one property or keyword name.
	maxSchemaKey = 256
)

// genericSchema is the permissive fallback for unusable schemas.
const genericSchema = `{"type":"object","additionalProperties":true}`

// schemaBudget counts the values a sanitizer may visit.
type schemaBudget struct {
	nodes int
}

func (b *schemaBudget) take() bool {
	b.nodes--
	return b.nodes >= 0
}

// schemaJSON validates, bounds and sanitizes a server input schema.
func schemaJSON(schema any) string {
	if schema == nil {
		return genericSchema
	}
	raw, err := json.Marshal(schema)
	if err != nil || len(raw) > MaxSchemaBytes {
		return genericSchema
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return genericSchema
	}
	object, ok := value.(map[string]any)
	if !ok {
		return genericSchema
	}
	budget := &schemaBudget{nodes: maxSchemaNodes}
	clean, ok := cleanSchemaNode(object, 0, budget)
	if !ok {
		return genericSchema
	}
	out, err := json.Marshal(clean)
	if err != nil || len(out) > MaxSchemaBytes {
		return genericSchema
	}
	return string(out)
}

// droppedSchemaAnnotations are annotation-only keywords. They have no
// effect on validation, so removing them preserves tool-call semantics
// while removing the largest free-form instruction surface.
var droppedSchemaAnnotations = map[string]bool{
	"description": true,
	"title":       true,
	"$comment":    true,
	"examples":    true,
	"default":     true,
}

// cleanSchemaNode validates one JSON value and returns its safe form.
func cleanSchemaNode(value any, depth int, budget *schemaBudget) (any, bool) {
	if depth > maxSchemaDepth || !budget.take() {
		return nil, false
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if !budget.take() || !validSchemaKey(key) {
				return nil, false
			}
			if droppedSchemaAnnotations[key] {
				continue
			}
			cleaned, ok := cleanSchemaKeyword(key, child, depth+1, budget)
			if !ok {
				return nil, false
			}
			out[key] = cleaned
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			cleaned, ok := cleanSchemaNode(item, depth+1, budget)
			if !ok {
				return nil, false
			}
			out = append(out, cleaned)
		}
		return out, true
	case string:
		// A semantic string (pattern, format, enum value, …) is kept
		// exactly as supplied: changing it would change validation. A
		// control character makes the schema unusable instead.
		if len(v) > maxSchemaString || hasControl(v) {
			return nil, false
		}
		return v, true
	case bool, float64, nil:
		return v, true
	default:
		return nil, false
	}
}

// cleanSchemaKeyword validates the shape of a known keyword. Unknown
// keywords are sanitized generically so vendor extensions keep working.
func cleanSchemaKeyword(key string, value any, depth int, budget *schemaBudget) (any, bool) {
	switch key {
	case "type":
		return cleanSchemaType(value)
	case "required", "dependentRequired":
		return cleanStringList(value, budget)
	case "properties", "patternProperties", "dependentSchemas", "$defs", "definitions":
		return cleanSchemaMap(value, depth, budget)
	case "allOf", "anyOf", "oneOf", "prefixItems":
		return cleanSchemaList(value, depth, budget)
	case "items", "additionalProperties", "additionalItems", "unevaluatedProperties",
		"unevaluatedItems", "contains", "propertyNames", "not", "if", "then", "else":
		return cleanSchemaOrBool(value, depth, budget)
	case "enum":
		return cleanEnum(value, depth, budget)
	case "const":
		return cleanInstanceValue(value, depth, budget)
	case "$ref", "$dynamicRef":
		ref, ok := value.(string)
		if !ok || len(ref) > maxSchemaString || hasControl(ref) {
			return nil, false
		}
		// Only local references are kept. A remote or relative reference
		// could point at another document; HAL does not need one to
		// describe a tool input, so the schema falls back instead.
		if !strings.HasPrefix(ref, "#") {
			return nil, false
		}
		return ref, true
	case "multipleOf":
		number, ok := value.(float64)
		if !ok || number <= 0 {
			return nil, false
		}
		return number, true
	case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties",
		"minContains", "maxContains":
		number, ok := value.(float64)
		if !ok || number < 0 || number != float64(int64(number)) {
			return nil, false
		}
		return number, true
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
		if _, ok := value.(float64); !ok {
			return nil, false
		}
		return value, true
	case "uniqueItems":
		if _, ok := value.(bool); !ok {
			return nil, false
		}
		return value, true
	default:
		return cleanSchemaNode(value, depth, budget)
	}
}

func cleanSchemaType(value any) (any, bool) {
	valid := func(name string) bool {
		switch name {
		case "null", "boolean", "object", "array", "number", "string", "integer":
			return true
		}
		return false
	}
	switch v := value.(type) {
	case string:
		if !valid(v) {
			return nil, false
		}
		return v, true
	case []any:
		if len(v) == 0 {
			return nil, false
		}
		out := make([]any, 0, len(v))
		for _, item := range v {
			name, ok := item.(string)
			if !ok || !valid(name) {
				return nil, false
			}
			out = append(out, name)
		}
		return out, true
	default:
		return nil, false
	}
}

func cleanStringList(value any, budget *schemaBudget) (any, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		if !budget.take() {
			return nil, false
		}
		name, ok := item.(string)
		if !ok || !validSchemaKey(name) {
			return nil, false
		}
		out = append(out, name)
	}
	return out, true
}

func cleanSchemaMap(value any, depth int, budget *schemaBudget) (any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]any, len(object))
	for key, child := range object {
		if !budget.take() || !validSchemaKey(key) {
			return nil, false
		}
		cleaned, ok := cleanSchemaOrBool(child, depth, budget)
		if !ok {
			return nil, false
		}
		out[key] = cleaned
	}
	return out, true
}

func cleanSchemaList(value any, depth int, budget *schemaBudget) (any, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		cleaned, ok := cleanSchemaOrBool(item, depth, budget)
		if !ok {
			return nil, false
		}
		out = append(out, cleaned)
	}
	return out, true
}

func cleanSchemaOrBool(value any, depth int, budget *schemaBudget) (any, bool) {
	if _, ok := value.(bool); ok {
		return value, true
	}
	return cleanSchemaNode(value, depth, budget)
}

func cleanEnum(value any, depth int, budget *schemaBudget) (any, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		cleaned, ok := cleanInstanceValue(item, depth, budget)
		if !ok {
			return nil, false
		}
		out = append(out, cleaned)
	}
	return out, true
}

// cleanInstanceValue validates a literal enumerated or constant value.
// Unlike a schema object, an instance object may legitimately contain a
// key named "description" or "title"; annotation fields are data there,
// so nothing is dropped.
func cleanInstanceValue(value any, depth int, budget *schemaBudget) (any, bool) {
	if depth > maxSchemaDepth || !budget.take() {
		return nil, false
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if !budget.take() || !validSchemaKey(key) {
				return nil, false
			}
			cleaned, ok := cleanInstanceValue(child, depth+1, budget)
			if !ok {
				return nil, false
			}
			out[key] = cleaned
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			cleaned, ok := cleanInstanceValue(item, depth+1, budget)
			if !ok {
				return nil, false
			}
			out = append(out, cleaned)
		}
		return out, true
	case string:
		if len(v) > maxSchemaString || hasControl(v) {
			return nil, false
		}
		return v, true
	case bool, float64, nil:
		return v, true
	default:
		return nil, false
	}
}

func validSchemaKey(key string) bool {
	return len(key) <= maxSchemaKey && !hasControl(key)
}

// hasControl reports whether a string carries a terminal control
// character. Such strings are refused from semantic positions rather
// than silently rewritten.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}
