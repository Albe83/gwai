package controlplane

import (
	"fmt"
	"regexp"
	"strings"
)

var providerSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type QualifiedModel struct {
	ProviderSlug  string
	UpstreamModel string
}

func (model QualifiedModel) String() string {
	return model.ProviderSlug + "/" + model.UpstreamModel
}

func ParseQualifiedModel(value string) (QualifiedModel, error) {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 || !providerSlugPattern.MatchString(parts[0]) {
		return QualifiedModel{}, &ValidationError{Field: "model", Message: "must use provider-slug/upstream-model format"}
	}
	upstreamModel := strings.TrimSpace(parts[1])
	if upstreamModel == "" || len(upstreamModel) > 300 {
		return QualifiedModel{}, &ValidationError{Field: "model", Message: "upstream model must contain between 1 and 300 bytes"}
	}
	if upstreamModel != parts[1] {
		return QualifiedModel{}, &ValidationError{Field: "model", Message: "must not contain leading or trailing whitespace"}
	}
	if strings.ContainsAny(upstreamModel, "\r\n\x00") {
		return QualifiedModel{}, &ValidationError{Field: "model", Message: "contains unsupported control characters"}
	}
	return QualifiedModel{ProviderSlug: parts[0], UpstreamModel: upstreamModel}, nil
}

func validateProviderSlug(slug string) error {
	if !providerSlugPattern.MatchString(slug) {
		return &ValidationError{Field: "slug", Message: fmt.Sprintf("%q must be a lowercase DNS label of at most 63 characters", slug)}
	}
	return nil
}
