package xrpc

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

//go:embed contracts.json
var contractJSON []byte

type contractNode struct {
	Type        string                   `json:"type"`
	Ref         string                   `json:"ref"`
	Description string                   `json:"description"`
	Properties  map[string]*contractNode `json:"properties"`
	Required    []string                 `json:"required"`
	Items       *contractNode            `json:"items"`
	MinLength   int                      `json:"minLength"`
	MaxLength   int                      `json:"maxLength"`
	Minimum     *int64                   `json:"minimum"`
	Maximum     *int64                   `json:"maximum"`
}

type transportContract struct {
	NSID       string        `json:"nsid"`
	Service    string        `json:"service"`
	Method     string        `json:"method"`
	RESTMethod string        `json:"restMethod"`
	RESTPath   string        `json:"restPath"`
	Admin      bool          `json:"admin"`
	NoOutput   bool          `json:"noOutput"`
	Envelope   string        `json:"envelope"`
	Input      *contractNode `json:"input"`
	Output     *contractNode `json:"output"`
}

var schemaDefinitions map[string]map[string]*contractNode

func loadOperations() []Operation {
	var bundle struct {
		Schemas map[string]struct {
			Defs map[string]*contractNode `json:"defs"`
		} `json:"schemas"`
		Operations []transportContract `json:"operations"`
	}
	if err := json.Unmarshal(contractJSON, &bundle); err != nil {
		panic("invalid generated XRPC contracts: " + err.Error())
	}
	schemaDefinitions = make(map[string]map[string]*contractNode, len(bundle.Schemas))
	for nsid, schema := range bundle.Schemas {
		schemaDefinitions[nsid] = schema.Defs
	}
	out := make([]Operation, 0, len(bundle.Operations))
	for _, c := range bundle.Operations {
		o := Operation{NSID: c.NSID, Service: c.Service, Method: c.Method, RESTMethod: c.RESTMethod, RESTPath: c.RESTPath, Admin: c.Admin, NoOutput: c.NoOutput, Envelope: c.Envelope, inputSchema: c.Input, outputSchema: c.Output}
		keys := make([]string, 0, len(c.Input.Properties))
		for key := range c.Input.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			n := c.Input.Properties[key]
			required := false
			for _, k := range c.Input.Required {
				required = required || key == k
			}
			o.Fields = append(o.Fields, Field{Name: key, Required: required, Boolean: n.Type == "boolean", MinLength: n.MinLength, MaxLength: n.MaxLength, kind: n.Type, schema: n})
		}
		out = append(out, o)
	}
	return out
}

func resolveNode(node *contractNode) (*contractNode, error) {
	for depth := 0; node != nil && node.Type == "ref"; depth++ {
		if depth > 32 {
			return nil, errors.New("recursive contract reference")
		}
		parts := strings.SplitN(node.Ref, "#", 2)
		fragment := "main"
		if len(parts) == 2 {
			fragment = parts[1]
		}
		node = schemaDefinitions[parts[0]][fragment]
	}
	if node == nil {
		return nil, errors.New("missing contract definition")
	}
	return node, nil
}

func decodeValue(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := decodeStrictValue(d, 0)
	if err != nil {
		return nil, errors.New("Invalid JSON value")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("Multiple JSON values are not allowed")
	}
	return value, nil
}

func decodeStrictValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("Input nesting exceeds contract limit")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("Invalid object key")
			}
			if _, exists := out[name]; exists {
				return nil, errors.New("Duplicate JSON property")
			}
			value, err := decodeStrictValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			value, err := decodeStrictValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, errors.New("Unexpected JSON delimiter")
	}
}

func validateContract(node *contractNode, value any, path string, depth int) error {
	if depth > 64 {
		return errors.New("Input nesting exceeds contract limit")
	}
	n, err := resolveNode(node)
	if err != nil {
		return err
	}
	fail := func(message string) error { return fmt.Errorf("%s: %s", path, message) }
	switch n.Type {
	case "string":
		v, ok := value.(string)
		if !ok {
			return fail("Expected string property")
		}
		if !utf8.ValidString(v) {
			return fail("Invalid UTF-8 argument")
		}
		count := utf8.RuneCountInString(v)
		if count < n.MinLength || n.MaxLength > 0 && count > n.MaxLength {
			return fail("Argument length is outside its contract")
		}
		if strings.HasPrefix(n.Description, "Finite base-10 decimal") {
			number, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return fail("Expected finite decimal string")
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("Expected boolean property")
		}
	case "integer":
		v, ok := value.(json.Number)
		if !ok {
			return fail("Expected integer property")
		}
		number, e := v.Int64()
		if e != nil {
			return fail("Expected signed 64-bit integer")
		}
		if n.Minimum != nil && number < *n.Minimum || n.Maximum != nil && number > *n.Maximum {
			return fail("Integer is outside its contract")
		}
	case "object", "params":
		v, ok := value.(map[string]any)
		if !ok {
			return fail("Expected object property")
		}
		for _, key := range n.Required {
			if _, ok := v[key]; !ok {
				return fail("Missing required argument: " + key)
			}
		}
		for key, child := range v {
			definition, ok := n.Properties[key]
			if !ok {
				return fail("Unknown property: " + key)
			}
			if err := validateContract(definition, child, path+"."+key, depth+1); err != nil {
				return err
			}
		}
	case "array":
		v, ok := value.([]any)
		if !ok {
			return fail("Expected array property")
		}
		if len(v) < n.MinLength || n.MaxLength > 0 && len(v) > n.MaxLength {
			return fail("Array length is outside its contract")
		}
		for _, item := range v {
			if err := validateContract(n.Items, item, path+"[]", depth+1); err != nil {
				return err
			}
		}
	default:
		return fail("Unsupported contract type")
	}
	return nil
}

func validateQuery(field Field, text string) error {
	var value any = text
	switch field.kind {
	case "boolean":
		if text != "true" && text != "false" {
			return errors.New("Expected boolean query parameter")
		}
		value = text == "true"
	case "integer":
		value = json.Number(text)
	}
	return validateContract(field.schema, value, field.Name, 0)
}

// NormalizeOutput creates the Lexicon view without mutating application data.
// SQL nulls are omitted; decimal measurements become base-10 strings; JSON schema
// documents retain explicit *_json fields. Unknown application fields are not
// copied into public views. Required fields and declared types are validated.
func NormalizeOutput(operation Operation, value any) (any, error) {
	if operation.NoOutput {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeValue(raw)
	if err != nil {
		return nil, err
	}
	return normalizeOutput(operation.outputSchema, decoded, 0)
}

func normalizeOutput(node *contractNode, value any, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("Output nesting exceeds contract limit")
	}
	n, err := resolveNode(node)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	switch n.Type {
	case "object":
		v, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("Application returned a non-object view")
		}
		out := map[string]any{}
		for key, definition := range n.Properties {
			child, exists := v[key]
			if !exists || child == nil {
				continue
			}
			normalized, e := normalizeOutput(definition, child, depth+1)
			if e != nil {
				return nil, fmt.Errorf("%s: %w", key, e)
			}
			if normalized != nil {
				out[key] = normalized
			}
		}
		for _, key := range n.Required {
			if _, ok := out[key]; !ok {
				return nil, fmt.Errorf("Application view missing %s", key)
			}
		}
		return out, nil
	case "array":
		v, ok := value.([]any)
		if !ok {
			return nil, errors.New("Application returned a non-array view")
		}
		out := make([]any, 0, len(v))
		for _, child := range v {
			item, e := normalizeOutput(n.Items, child, depth+1)
			if e != nil {
				return nil, e
			}
			out = append(out, item)
		}
		return out, nil
	case "string":
		if number, ok := value.(json.Number); ok {
			if strings.HasPrefix(n.Description, "Finite base-10 decimal") {
				f, e := number.Float64()
				if e != nil || math.IsInf(f, 0) || math.IsNaN(f) {
					return nil, errors.New("Non-finite decimal measurement")
				}
				return strconv.FormatFloat(f, 'f', -1, 64), nil
			}
		}
	}
	if err := validateContract(n, value, "output", depth); err != nil {
		return nil, err
	}
	return value, nil
}
