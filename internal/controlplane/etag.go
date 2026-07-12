package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// entityETag returns a strong validator for the public resource representation.
// The digest includes server-owned fields, so every observable resource change
// produces a different validator without exposing repository implementation
// details such as Dapr ETags.
func entityETag(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode resource ETag: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return `"` + hex.EncodeToString(digest[:]) + `"`, nil
}

type parsedEntityTag struct {
	value string
	weak  bool
}

type ifMatchPrecondition struct {
	present bool
	value   string
}

func optionalIfMatch(value string) ifMatchPrecondition {
	return ifMatchPrecondition{present: value != "", value: value}
}

// parseIfMatch accepts the RFC entity-tag list form, including a wildcard.
// Weak validators are syntactically valid but never satisfy If-Match's strong
// comparison requirement.
func parseIfMatch(value string) ([]parsedEntityTag, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false, &ValidationError{Field: "if_match", Message: "must be * or one or more valid entity tags"}
	}
	if value == "*" {
		return nil, true, nil
	}

	tags := make([]parsedEntityTag, 0, 1)
	for offset := 0; offset < len(value); {
		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
			offset++
		}
		weak := false
		if offset+2 <= len(value) && value[offset:offset+2] == "W/" {
			weak = true
			offset += 2
		}
		if offset >= len(value) || value[offset] != '"' {
			return nil, false, &ValidationError{Field: "if_match", Message: "must be * or one or more valid entity tags"}
		}
		start := offset
		offset++
		for offset < len(value) && value[offset] != '"' {
			character := value[offset]
			if character != 0x21 && (character < 0x23 || character > 0x7e) && character < 0x80 {
				return nil, false, &ValidationError{Field: "if_match", Message: "contains an invalid entity tag"}
			}
			offset++
		}
		if offset >= len(value) {
			return nil, false, &ValidationError{Field: "if_match", Message: "contains an unterminated entity tag"}
		}
		offset++
		tags = append(tags, parsedEntityTag{value: value[start:offset], weak: weak})

		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
			offset++
		}
		if offset == len(value) {
			break
		}
		if value[offset] != ',' {
			return nil, false, &ValidationError{Field: "if_match", Message: "must separate entity tags with commas"}
		}
		offset++
		if offset == len(value) {
			return nil, false, &ValidationError{Field: "if_match", Message: "must not end with a comma"}
		}
	}
	if len(tags) == 0 {
		return nil, false, &ValidationError{Field: "if_match", Message: "must contain an entity tag"}
	}
	return tags, false, nil
}

// enforceIfMatch evaluates a caller precondition against the state already
// loaded by the service. A missing header preserves the original unconditional
// update contract; repository expected-record CAS still protects the later
// write from a race after this check.
func enforceIfMatch(precondition ifMatchPrecondition, current any) error {
	if !precondition.present {
		return nil
	}
	tags, wildcard, err := parseIfMatch(precondition.value)
	if err != nil {
		return err
	}
	if wildcard {
		return nil
	}
	currentTag, err := entityETag(current)
	if err != nil {
		return err
	}
	for _, candidate := range tags {
		if !candidate.weak && candidate.value == currentTag {
			return nil
		}
	}
	return fmt.Errorf("%w: resource changed since it was loaded", ErrConflict)
}
