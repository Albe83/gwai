package ir

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedSchemaIsValidJSONAndMatchesVersion(t *testing.T) {
	data, err := os.ReadFile("../../api/ir/2026-07-01.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("published IR schema is not valid JSON: %v", err)
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("published IR schema has no $defs")
	}
	request := definitions["request"].(map[string]any)
	properties := request["properties"].(map[string]any)
	version := properties["version"].(map[string]any)["const"]
	if version != Version {
		t.Fatalf("schema version %v does not match Go version %s", version, Version)
	}
}
