package controlplane

import (
	"fmt"
	"regexp"
)

var providerSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validateProviderSlug(slug string) error {
	if !providerSlugPattern.MatchString(slug) {
		return &ValidationError{Field: "slug", Message: fmt.Sprintf("%q must be a lowercase DNS label of at most 63 characters", slug)}
	}
	return nil
}
