package agent

import (
	"encoding/json"
	"reflect"
	"strings"
)

// SchemaFromType generates a JSON Schema from a Go type using reflection.
// It reads `json` tags for field names and `desc` tags for descriptions.
func SchemaFromType(t reflect.Type) json.RawMessage {
	schema := buildSchema(t)
	data, _ := json.Marshal(schema)
	return data
}

func buildSchema(t reflect.Type) map[string]interface{} {
	// Dereference pointers
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Struct:
		return buildObjectSchema(t)
	case reflect.Slice, reflect.Array:
		return buildArraySchema(t)
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]interface{}{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

func buildObjectSchema(t reflect.Type) map[string]interface{} {
	properties := make(map[string]interface{})
	required := []string{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Get JSON field name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		jsonName := strings.Split(jsonTag, ",")[0]
		isOmitempty := strings.Contains(jsonTag, "omitempty")

		// Build field schema
		fieldSchema := buildSchema(field.Type)

		// Add description from desc tag
		if desc := field.Tag.Get("desc"); desc != "" {
			fieldSchema["description"] = desc
		}

		properties[jsonName] = fieldSchema

		// Add to required unless omitempty
		if !isOmitempty {
			required = append(required, jsonName)
		}
	}

	schema := map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

func buildArraySchema(t reflect.Type) map[string]interface{} {
	return map[string]interface{}{
		"type":  "array",
		"items": buildSchema(t.Elem()),
	}
}

// ProposerResponseSchema returns the JSON schema for ProposerResponse.
func ProposerResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(ProposerResponse{}))
}

// VariationProposerResponseSchema returns the JSON schema for VariationProposerResponse.
func VariationProposerResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(VariationProposerResponse{}))
}

// EvaluationCriteriaResponseSchema returns the JSON schema for EvaluationCriteriaResponse.
func EvaluationCriteriaResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(EvaluationCriteriaResponse{}))
}

// VariationEvaluationResponseSchema returns the JSON schema for VariationEvaluationResponse.
func VariationEvaluationResponseSchema() json.RawMessage {
	return SchemaFromType(reflect.TypeOf(VariationEvaluationResponse{}))
}
