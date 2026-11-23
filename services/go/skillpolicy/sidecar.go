// Package skillpolicy merges portable source policies for repository providers.
package skillpolicy

import (
	"encoding/hex"
	"errors"
	"go.yaml.in/yaml/v3"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrClarification = errors.New("Resolve runtime clarification questions before writeback")
var skillID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func MergeSidecar(existing []byte, document map[string]any, exposure string) ([]byte, error) {
	if len(existing) > 256<<10 || !utf8.Valid(existing) {
		return nil, errors.New("existing skill policy is oversized or invalid UTF-8")
	}
	id, _ := document["id"].(string)
	if !skillID.MatchString(id) || len(id) > 64 {
		return nil, errors.New("invalid canonical skill id")
	}
	if exposure != "tool" && exposure != "resource" && exposure != "prompt" {
		return nil, errors.New("invalid canonical exposure")
	}
	validation, _ := document["validation"].(map[string]any)
	if validation == nil || validation["clarificationRequired"] != false {
		return nil, ErrClarification
	}
	root := map[string]any{}
	if len(existing) > 0 {
		decoder := yaml.NewDecoder(strings.NewReader(string(existing)))
		if err := decoder.Decode(&root); err != nil {
			return nil, errors.New("existing policy YAML is invalid")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, errors.New("existing policy must be one YAML document")
		}
		if root == nil {
			return nil, errors.New("existing policy root must be a mapping")
		}
		if version, ok := root["version"]; ok && version != 1 {
			return nil, errors.New("unsupported existing policy version")
		}
	}
	skills := map[string]any{}
	if existing, ok := root["skills"]; ok {
		var valid bool
		skills, valid = existing.(map[string]any)
		if !valid {
			return nil, errors.New("existing policy skills must be a mapping")
		}
	}
	source, _ := document["source"].(map[string]any)
	checksum, _ := source["checksum"].(string)
	if decoded, err := hex.DecodeString(checksum); err != nil || len(decoded) != 32 {
		return nil, errors.New("canonical checksum is missing")
	}
	metadata := map[string]any{"exposure": exposure}
	for _, key := range []string{"kind", "scope", "activation", "enforcement", "priority", "requires", "version", "lifecycle"} {
		if value, ok := document[key]; ok && value != nil {
			metadata[key] = value
		}
	}
	if value, ok := document["avoidWhen"]; ok {
		metadata["avoid_when"] = value
	}
	if value, ok := document["conflictsWith"]; ok {
		metadata["conflicts_with"] = value
	}
	generated := map[string]any{"base_checksum": checksum, "metadata": metadata}
	prior, _ := skills[id].(map[string]any)
	skills[id] = mergeMapping(prior, generated)
	root["version"] = 1
	root["skills"] = skills
	result, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	if len(result) > 256<<10 {
		return nil, errors.New("merged policy exceeds 256 KiB")
	}
	return result, nil
}
func mergeMapping(existing, generated map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range generated {
		if nested, ok := v.(map[string]any); ok {
			prior, _ := out[k].(map[string]any)
			out[k] = mergeMapping(prior, nested)
		} else {
			out[k] = v
		}
	}
	return out
}
