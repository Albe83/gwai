package controlplane

import "testing"

func TestParseQualifiedModelSplitsOnlyFirstSlash(t *testing.T) {
	model, err := ParseQualifiedModel("team-a/anthropic/claude-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if model.ProviderSlug != "team-a" || model.UpstreamModel != "anthropic/claude-sonnet" {
		t.Fatalf("unexpected parsed model: %+v", model)
	}
}

func TestParseQualifiedModelRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"claude", "/claude", "Team/claude", "team/", "team/ claude"} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseQualifiedModel(value); err == nil {
				t.Fatalf("expected %q to be rejected", value)
			}
		})
	}
}
