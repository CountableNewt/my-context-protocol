// Package session implements the existing app_sessions payload format.
package session

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

type Payload map[string]string

func Encode(payload Payload) (string, error) {
	if payload == nil {
		payload = Payload{}
	}
	encoded, err := json.Marshal(payload)
	return string(encoded), err
}

func Decode(encoded string) (Payload, error) {
	// Decode individual strings explicitly: JSON null is accepted by Go's string
	// decoder but rejected by Swift's [String:String] decoder.
	if !utf8.ValidString(encoded) {
		return nil, errors.New("session payload must be valid UTF-8")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("session payload must be an object")
	}
	result := make(Payload, len(raw))
	for key, value := range raw {
		if len(value) == 0 || value[0] != '"' {
			return nil, errors.New("session payload values must be strings")
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, err
		}
		result[key] = decoded
	}
	return result, nil
}
