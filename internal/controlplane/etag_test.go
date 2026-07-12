package controlplane

import (
	"errors"
	"testing"
)

func TestStrongETagAndIfMatchSemantics(t *testing.T) {
	current := User{ID: "usr_one", Name: "Ada", Email: "ada@example.com", Status: StatusActive, Revision: 1}
	etag, err := entityETag(current)
	if err != nil {
		t.Fatal(err)
	}
	same, err := entityETag(current)
	if err != nil || same != etag {
		t.Fatalf("ETag is not deterministic: first=%q second=%q err=%v", etag, same, err)
	}
	changed := current
	changed.Revision++
	changedETag, err := entityETag(changed)
	if err != nil || changedETag == etag {
		t.Fatalf("changed representation retained ETag %q", etag)
	}

	if err := enforceIfMatch(ifMatchPrecondition{}, current); err != nil {
		t.Fatalf("missing If-Match rejected current representation: %v", err)
	}
	for _, header := range []string{"*", etag, `"stale", ` + etag, `"contains,a,comma", ` + etag} {
		if err := enforceIfMatch(ifMatchPrecondition{present: true, value: header}, current); err != nil {
			t.Errorf("If-Match %q rejected current representation: %v", header, err)
		}
	}
	for _, header := range []string{`"stale"`, "W/" + etag} {
		if err := enforceIfMatch(ifMatchPrecondition{present: true, value: header}, current); !errors.Is(err, ErrConflict) {
			t.Errorf("If-Match %q returned %v, want conflict", header, err)
		}
	}
	for _, header := range []string{"", "unquoted", `"unterminated`, `"tag" trailing`, `"tag",`, `*, "tag"`} {
		err := enforceIfMatch(ifMatchPrecondition{present: true, value: header}, current)
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Field != "if_match" {
			t.Errorf("If-Match %q returned %v, want if_match validation error", header, err)
		}
	}
}

func TestIfMatchIsEvaluatedAfterResourceLoad(t *testing.T) {
	planes := newTestControlPlanes()
	_, err := planes.resources.UpdateUserIfMatch(t.Context(), "usr_missing", UserInput{
		Name: "Missing", Email: "missing@example.com", Status: StatusActive,
	}, "malformed")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing resource returned %v, want not found before If-Match evaluation", err)
	}
}

func TestConditionalDeleteServiceMethods(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		planes := newTestControlPlanes()
		user, _, _ := provisionTestRoute(t, planes)
		etag, err := entityETag(user)
		if err != nil {
			t.Fatal(err)
		}
		if err := planes.resources.DeleteUserIfMatch(t.Context(), user.ID, `"stale"`); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale user delete returned %v, want conflict", err)
		}
		if err := planes.resources.DeleteUserIfMatch(t.Context(), user.ID, etag); err != nil {
			t.Fatalf("matching user delete failed: %v", err)
		}
	})

	t.Run("provider", func(t *testing.T) {
		planes := newTestControlPlanes()
		_, provider, model := provisionTestRoute(t, planes)
		if err := planes.resources.DeleteModel(t.Context(), model.ID); err != nil {
			t.Fatal(err)
		}
		etag, err := entityETag(provider)
		if err != nil {
			t.Fatal(err)
		}
		if err := planes.resources.DeleteProviderIfMatch(t.Context(), provider.ID, `"stale"`); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale provider delete returned %v, want conflict", err)
		}
		if err := planes.resources.DeleteProviderIfMatch(t.Context(), provider.ID, etag); err != nil {
			t.Fatalf("matching provider delete failed: %v", err)
		}
	})

	t.Run("virtual key", func(t *testing.T) {
		planes := newTestControlPlanes()
		user, _, model := provisionTestRoute(t, planes)
		created, err := planes.keys.CreateVirtualKey(t.Context(), VirtualKeyInput{
			Name: "CLI", UserID: user.ID, ModelIDs: []string{model.ID}, Status: StatusActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		etag, err := entityETag(created.VirtualKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := planes.keys.DeleteVirtualKeyIfMatch(t.Context(), created.VirtualKey.ID, `"stale"`); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale virtual-key delete returned %v, want conflict", err)
		}
		if err := planes.keys.DeleteVirtualKeyIfMatch(t.Context(), created.VirtualKey.ID, etag); err != nil {
			t.Fatalf("matching virtual-key delete failed: %v", err)
		}
	})
}
