package adminui

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/Albe83/gwai/internal/controlplane"
)

func warningMessage(prefix string, err error) string {
	if err == nil {
		return ""
	}
	if view, _ := errorView(err); view != nil && view.Detail != "" {
		return prefix + ": " + view.Detail
	}
	return prefix + "."
}

func validLifecycleStatus(value string) bool {
	return value == string(controlplane.StatusActive) || value == string(controlplane.StatusDisabled)
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Users", "users")
	var users []controlplane.User
	var keys []controlplane.PublicVirtualKey
	var err, keyErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		users, err = s.api.ListUsers(r.Context())
	}()
	go func() {
		defer wait.Done()
		keys, keyErr = s.api.ListVirtualKeys(r.Context())
	}()
	wait.Wait()
	if err != nil {
		s.renderOperationError(w, session, data, "users", err)
		return
	}
	data.RelationsAvailable = keyErr == nil
	if keyErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Virtual-key relationships are unavailable", keyErr))
	}
	data.UserKeyCounts = make(map[string]int)
	for _, key := range keys {
		data.UserKeyCounts[key.UserID]++
	}
	data.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	query := strings.ToLower(data.Query)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	data.StatusFilter = status
	for _, user := range users {
		if query != "" && !strings.Contains(strings.ToLower(user.Name+" "+user.Email+" "+user.ID), query) {
			continue
		}
		if status != "" && string(user.Status) != status {
			continue
		}
		data.Users = append(data.Users, user)
	}
	slices.SortFunc(data.Users, func(left, right controlplane.User) int {
		return cmp.Or(strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), strings.Compare(left.ID, right.ID))
	})
	s.views.render(w, http.StatusOK, "users", data)
}

func (s *server) newUser(w http.ResponseWriter, _ *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Create user", "users")
	data.UserForm = &userForm{Status: string(controlplane.StatusActive)}
	s.views.render(w, http.StatusOK, "user-form", data)
}

func userFormFromRequest(r *http.Request) userForm {
	return userForm{
		Name: formString(r, "name"), Email: formString(r, "email"),
		Status: statusValue(r.PostForm.Get("status")),
	}
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	form := userFormFromRequest(r)
	if !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Create user", "users")
		data.UserForm = &form
		s.renderInvalidForm(w, data, "user-form", "Status must be active or disabled.")
		return
	}
	if _, err := s.api.CreateUser(r.Context(), form.input()); err != nil {
		data := s.basePage(session, "Create user", "users")
		data.UserForm = &form
		s.renderOperationError(w, session, data, "user-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/users", "success", "User created.")
}

func (s *server) editUser(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetUser(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Edit user", "users")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	user := versioned.Value
	form := userFormFromModel(user)
	data.UserForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, user.ID, versioned.ETag
	s.views.render(w, http.StatusOK, "user-form", data)
}

func (s *server) updateUser(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id := r.PathValue("id")
	etag := formString(r, "_etag")
	form := userFormFromRequest(r)
	if etag == "" || !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Edit user", "users")
		data.UserForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.renderInvalidForm(w, data, "user-form", "A current resource version and an active or disabled status are required. Reload the edit page and try again.")
		return
	}
	if _, err := s.api.UpdateUser(r.Context(), id, form.input(), etag); err != nil {
		data := s.basePage(session, "Edit user", "users")
		data.UserForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.renderOperationError(w, session, data, "user-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/users", "success", "User updated. Authorization revision advanced.")
}

func (s *server) confirmUserStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	status := strings.TrimSpace(r.URL.Query().Get("to"))
	if !validLifecycleStatus(status) {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "Status must be active or disabled.")
		return
	}
	versioned, err := s.api.GetUser(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Change user status", "users")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	warning := "Enabling the user can restore authorization for their active, non-expired keys."
	if status == string(controlplane.StatusDisabled) {
		warning = "Disabling the user immediately revokes every virtual key they own."
	}
	data.Lifecycle = &lifecycleView{
		Kind: "user", Name: versioned.Value.Name, Status: status,
		Action: "/users/" + versioned.Value.ID + "/status", Cancel: "/users",
		Warning: warning, ButtonText: strings.ToUpper(status[:1]) + status[1:] + " user", ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-status", data)
}

func (s *server) changeUserStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	status, etag := formString(r, "status"), formString(r, "_etag")
	if !validLifecycleStatus(status) || etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "A current resource version and an active or disabled status are required. Reload the confirmation page and try again.")
		return
	}
	id := r.PathValue("id")
	versioned, err := s.api.GetUser(r.Context(), id)
	if err == nil {
		user := versioned.Value
		_, err = s.api.UpdateUser(r.Context(), id, controlplane.UserInput{
			Name: user.Name, Email: user.Email, Status: controlplane.Status(status),
		}, etag)
	}
	if err != nil {
		data := s.basePage(session, "Change user status", "users")
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	message := "User enabled. Active keys can authorize again."
	if status == string(controlplane.StatusDisabled) {
		message = "User disabled. All of the user's keys are revoked."
	}
	s.flashRedirect(w, r, session, "/users", "success", message)
}

func (s *server) confirmDeleteUser(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetUser(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Delete user", "users")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	user := versioned.Value
	warning := "Deletion creates a permanent authorization fence. This action cannot be undone."
	if keys, keyErr := s.api.ListVirtualKeys(r.Context()); keyErr == nil {
		count := 0
		for _, key := range keys {
			if key.UserID == user.ID {
				count++
			}
		}
		if count > 0 {
			warning = fmt.Sprintf("This user still owns %d virtual key(s). Delete or reassign every key before deleting the user.", count)
		}
	} else {
		data.Warnings = append(data.Warnings, warningMessage("Could not inspect virtual-key ownership", keyErr))
	}
	data.Confirm = &confirmView{
		Kind: "user", Name: user.Name, Action: "/users/" + user.ID + "/delete",
		Cancel: "/users", Warning: warning, ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-delete", data)
}

func (s *server) deleteUser(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id := r.PathValue("id")
	etag := formString(r, "_etag")
	if etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid deletion", "The resource version is missing. Reload the confirmation page and try again.")
		return
	}
	if err := s.api.DeleteUser(r.Context(), id, etag); err != nil {
		data := s.basePage(session, "Delete user", "users")
		data.Confirm = &confirmView{
			Kind: "user", Name: id, Action: "/users/" + id + "/delete", Cancel: "/users",
			Warning: "The user can be deleted only after all virtual keys have been deleted or reassigned.", ETag: etag,
		}
		s.renderOperationError(w, session, data, "confirm-delete", err)
		return
	}
	s.flashRedirect(w, r, session, "/users", "success", "User deleted and permanently fenced.")
}

func (s *server) listProviders(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Providers", "providers")
	var providers []controlplane.Provider
	var models []controlplane.Model
	var err, modelErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		providers, err = s.api.ListProviders(r.Context())
	}()
	go func() {
		defer wait.Done()
		models, modelErr = s.api.ListModels(r.Context())
	}()
	wait.Wait()
	if err != nil {
		s.renderOperationError(w, session, data, "providers", err)
		return
	}
	data.RelationsAvailable = modelErr == nil
	data.ProviderModelCounts = make(map[string]int)
	for _, model := range models {
		data.ProviderModelCounts[model.ProviderID]++
	}
	if modelErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Model relationships are unavailable", modelErr))
	}
	data.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	query := strings.ToLower(data.Query)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	data.StatusFilter = status
	for _, provider := range providers {
		if query != "" && !strings.Contains(strings.ToLower(provider.Name+" "+provider.Slug+" "+provider.Kind+" "+provider.AdapterAppID), query) {
			continue
		}
		if status != "" && string(provider.Status) != status {
			continue
		}
		data.Providers = append(data.Providers, provider)
	}
	slices.SortFunc(data.Providers, func(left, right controlplane.Provider) int {
		return strings.Compare(left.Slug, right.Slug)
	})
	s.views.render(w, http.StatusOK, "providers", data)
}

func (s *server) newProvider(w http.ResponseWriter, _ *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Create provider", "providers")
	data.ProviderForm = &providerForm{
		Kind: controlplane.ProviderKindAnthropic, Status: string(controlplane.StatusActive),
	}
	s.views.render(w, http.StatusOK, "provider-form", data)
}

func providerFormFromRequest(r *http.Request) providerForm {
	return providerForm{
		Slug: formString(r, "slug"), Name: formString(r, "name"), Kind: formString(r, "kind"),
		AdapterAppID: formString(r, "adapter_app_id"), Status: statusValue(r.PostForm.Get("status")),
	}
}

func (s *server) createProvider(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	form := providerFormFromRequest(r)
	if !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Create provider", "providers")
		data.ProviderForm = &form
		s.renderInvalidForm(w, data, "provider-form", "Status must be active or disabled.")
		return
	}
	if _, err := s.api.CreateProvider(r.Context(), form.input()); err != nil {
		data := s.basePage(session, "Create provider", "providers")
		data.ProviderForm = &form
		s.renderOperationError(w, session, data, "provider-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/providers", "success", "Provider registered. Ensure the matching adapter is deployed.")
}

func (s *server) editProvider(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetProvider(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Edit provider", "providers")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	provider := versioned.Value
	form := providerFormFromModel(provider)
	data.ProviderForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, provider.ID, versioned.ETag
	s.views.render(w, http.StatusOK, "provider-form", data)
}

func (s *server) updateProvider(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id := r.PathValue("id")
	etag := formString(r, "_etag")
	form := providerFormFromRequest(r)
	if etag == "" || !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Edit provider", "providers")
		data.ProviderForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.renderInvalidForm(w, data, "provider-form", "A current resource version and an active or disabled status are required. Reload the edit page and try again.")
		return
	}
	if _, err := s.api.UpdateProvider(r.Context(), id, form.input(), etag); err != nil {
		data := s.basePage(session, "Edit provider", "providers")
		data.ProviderForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.renderOperationError(w, session, data, "provider-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/providers", "success", "Provider updated.")
}

func (s *server) confirmProviderStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	status := strings.TrimSpace(r.URL.Query().Get("to"))
	if !validLifecycleStatus(status) {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "Status must be active or disabled.")
		return
	}
	versioned, err := s.api.GetProvider(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Change provider status", "providers")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	warning := "Enabling the provider makes its active models routable again."
	if status == string(controlplane.StatusDisabled) {
		warning = "Disabling the provider immediately makes all of its models unroutable."
	}
	data.Lifecycle = &lifecycleView{
		Kind: "provider", Name: versioned.Value.Name, Status: status,
		Action: "/providers/" + versioned.Value.ID + "/status", Cancel: "/providers",
		Warning: warning, ButtonText: strings.ToUpper(status[:1]) + status[1:] + " provider", ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-status", data)
}

func (s *server) changeProviderStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	status, etag := formString(r, "status"), formString(r, "_etag")
	if !validLifecycleStatus(status) || etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "A current resource version and an active or disabled status are required. Reload the confirmation page and try again.")
		return
	}
	id := r.PathValue("id")
	versioned, err := s.api.GetProvider(r.Context(), id)
	if err == nil {
		provider := versioned.Value
		input := providerFormFromModel(provider).input()
		input.Status = controlplane.Status(status)
		_, err = s.api.UpdateProvider(r.Context(), id, input, etag)
	}
	if err != nil {
		data := s.basePage(session, "Change provider status", "providers")
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	message := "Provider enabled."
	if status == string(controlplane.StatusDisabled) {
		message = "Provider disabled. Its models are no longer routable."
	}
	s.flashRedirect(w, r, session, "/providers", "success", message)
}

func (s *server) confirmDeleteProvider(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetProvider(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Delete provider", "providers")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	provider := versioned.Value
	warning := "The provider can be deleted only when no model references it."
	if models, modelErr := s.api.ListModels(r.Context()); modelErr == nil {
		count := 0
		for _, model := range models {
			if model.ProviderID == provider.ID {
				count++
			}
		}
		if count > 0 {
			warning = fmt.Sprintf("This provider still owns %d model(s). Delete or reassign every model before deleting the provider.", count)
		}
	} else {
		data.Warnings = append(data.Warnings, warningMessage("Could not inspect model relationships", modelErr))
	}
	data.Confirm = &confirmView{
		Kind: "provider", Name: provider.Name, Action: "/providers/" + provider.ID + "/delete",
		Cancel: "/providers", Warning: warning, ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-delete", data)
}

func (s *server) deleteProvider(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id := r.PathValue("id")
	etag := formString(r, "_etag")
	if etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid deletion", "The resource version is missing. Reload the confirmation page and try again.")
		return
	}
	if err := s.api.DeleteProvider(r.Context(), id, etag); err != nil {
		data := s.basePage(session, "Delete provider", "providers")
		data.Confirm = &confirmView{Kind: "provider", Name: id, Action: "/providers/" + id + "/delete", Cancel: "/providers", Warning: "The provider was not deleted.", ETag: etag}
		s.renderOperationError(w, session, data, "confirm-delete", err)
		return
	}
	s.flashRedirect(w, r, session, "/providers", "success", "Provider deleted.")
}

func (s *server) modelReferenceData(ctx *http.Request, data *pageData) {
	providers, err := s.api.ListProviders(ctx.Context())
	data.Providers = providers
	data.ProviderNames = make(map[string]string, len(providers))
	for _, provider := range providers {
		data.ProviderNames[provider.ID] = provider.Name
	}
	if err != nil {
		data.Warnings = append(data.Warnings, warningMessage("Provider choices are unavailable", err))
	}
	slices.SortFunc(data.Providers, func(left, right controlplane.Provider) int {
		return cmp.Or(strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), strings.Compare(left.ID, right.ID))
	})
}

func (s *server) listModels(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Models", "models")
	var models []controlplane.Model
	var providers []controlplane.Provider
	var keys []controlplane.PublicVirtualKey
	var err, providerErr, keyErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		models, err = s.api.ListModels(r.Context())
	}()
	go func() {
		defer wait.Done()
		providers, providerErr = s.api.ListProviders(r.Context())
	}()
	go func() {
		defer wait.Done()
		keys, keyErr = s.api.ListVirtualKeys(r.Context())
	}()
	wait.Wait()
	if err != nil {
		s.renderOperationError(w, session, data, "models", err)
		return
	}
	data.ProviderNames = make(map[string]string, len(providers))
	for _, provider := range providers {
		data.ProviderNames[provider.ID] = provider.Name
	}
	data.ModelKeyCounts = make(map[string]int)
	for _, key := range keys {
		for _, modelID := range key.ModelIDs {
			data.ModelKeyCounts[modelID]++
		}
	}
	data.RelationsAvailable = providerErr == nil && keyErr == nil
	if providerErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Provider names are unavailable", providerErr))
	}
	if keyErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Virtual-key relationships are unavailable", keyErr))
	}
	data.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	query := strings.ToLower(data.Query)
	data.StatusFilter = strings.TrimSpace(r.URL.Query().Get("status"))
	data.ProviderFilter = strings.TrimSpace(r.URL.Query().Get("provider_id"))
	data.Providers = providers
	slices.SortFunc(data.Providers, func(left, right controlplane.Provider) int {
		return cmp.Or(strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), strings.Compare(left.ID, right.ID))
	})
	for _, model := range models {
		searchable := model.Alias + " " + model.UpstreamModel + " " + model.ID + " " + model.ProviderID + " " + data.ProviderNames[model.ProviderID]
		if query != "" && !strings.Contains(strings.ToLower(searchable), query) {
			continue
		}
		if data.StatusFilter != "" && string(model.Status) != data.StatusFilter {
			continue
		}
		if data.ProviderFilter != "" && model.ProviderID != data.ProviderFilter {
			continue
		}
		data.Models = append(data.Models, model)
	}
	slices.SortFunc(data.Models, func(left, right controlplane.Model) int {
		return cmp.Or(strings.Compare(strings.ToLower(left.Alias), strings.ToLower(right.Alias)), strings.Compare(left.ID, right.ID))
	})
	s.views.render(w, http.StatusOK, "models", data)
}

func (s *server) newModel(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Create model", "models")
	data.ModelForm = &modelForm{Status: string(controlplane.StatusActive)}
	s.modelReferenceData(r, &data)
	s.views.render(w, http.StatusOK, "model-form", data)
}

func modelFormFromRequest(r *http.Request) modelForm {
	return modelForm{
		Alias: formString(r, "alias"), ProviderID: formString(r, "provider_id"),
		UpstreamModel: formString(r, "upstream_model"), Status: statusValue(r.PostForm.Get("status")),
	}
}

func (s *server) createModel(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	form := modelFormFromRequest(r)
	if !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Create model", "models")
		data.ModelForm = &form
		s.modelReferenceData(r, &data)
		s.renderInvalidForm(w, data, "model-form", "Status must be active or disabled.")
		return
	}
	if _, err := s.api.CreateModel(r.Context(), form.input()); err != nil {
		data := s.basePage(session, "Create model", "models")
		data.ModelForm = &form
		s.modelReferenceData(r, &data)
		s.renderOperationError(w, session, data, "model-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/models", "success", "Model created.")
}

func (s *server) editModel(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetModel(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Edit model", "models")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	model := versioned.Value
	form := modelFormFromModel(model)
	data.ModelForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, model.ID, versioned.ETag
	s.modelReferenceData(r, &data)
	s.views.render(w, http.StatusOK, "model-form", data)
}

func (s *server) updateModel(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id, etag := r.PathValue("id"), formString(r, "_etag")
	form := modelFormFromRequest(r)
	if etag == "" || !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Edit model", "models")
		data.ModelForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.modelReferenceData(r, &data)
		s.renderInvalidForm(w, data, "model-form", "A current resource version and an active or disabled status are required. Reload the edit page and try again.")
		return
	}
	if _, err := s.api.UpdateModel(r.Context(), id, form.input(), etag); err != nil {
		data := s.basePage(session, "Edit model", "models")
		data.ModelForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.modelReferenceData(r, &data)
		s.renderOperationError(w, session, data, "model-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/models", "success", "Model updated.")
}

func (s *server) confirmModelStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	status := strings.TrimSpace(r.URL.Query().Get("to"))
	if !validLifecycleStatus(status) {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "Status must be active or disabled.")
		return
	}
	versioned, err := s.api.GetModel(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Change model status", "models")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	warning := "Enabling the model makes it routable for active virtual keys that reference it."
	if status == string(controlplane.StatusDisabled) {
		warning = "Disabling the model immediately makes it unroutable for every virtual key."
	}
	data.Lifecycle = &lifecycleView{
		Kind: "model", Name: versioned.Value.Alias, Status: status,
		Action: "/models/" + versioned.Value.ID + "/status", Cancel: "/models",
		Warning: warning, ButtonText: strings.ToUpper(status[:1]) + status[1:] + " model", ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-status", data)
}

func (s *server) changeModelStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	status, etag := formString(r, "status"), formString(r, "_etag")
	if !validLifecycleStatus(status) || etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "A current resource version and an active or disabled status are required. Reload the confirmation page and try again.")
		return
	}
	id := r.PathValue("id")
	versioned, err := s.api.GetModel(r.Context(), id)
	if err == nil {
		input := modelFormFromModel(versioned.Value).input()
		input.Status = controlplane.Status(status)
		_, err = s.api.UpdateModel(r.Context(), id, input, etag)
	}
	if err != nil {
		data := s.basePage(session, "Change model status", "models")
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	message := "Model enabled."
	if status == string(controlplane.StatusDisabled) {
		message = "Model disabled and unroutable."
	}
	s.flashRedirect(w, r, session, "/models", "success", message)
}

func (s *server) confirmDeleteModel(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetModel(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Delete model", "models")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	model := versioned.Value
	warning := "The model can be deleted only when no virtual key references it."
	if keys, keyErr := s.api.ListVirtualKeys(r.Context()); keyErr == nil {
		count := 0
		for _, key := range keys {
			if slices.Contains(key.ModelIDs, model.ID) {
				count++
			}
		}
		if count > 0 {
			warning = fmt.Sprintf("This model is still referenced by %d virtual key(s). Remove it from every key before deleting the model.", count)
		}
	} else {
		data.Warnings = append(data.Warnings, warningMessage("Could not inspect virtual-key relationships", keyErr))
	}
	data.Confirm = &confirmView{
		Kind: "model", Name: model.Alias, Action: "/models/" + model.ID + "/delete",
		Cancel: "/models", Warning: warning, ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-delete", data)
}

func (s *server) deleteModel(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id, etag := r.PathValue("id"), formString(r, "_etag")
	if etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid deletion", "The resource version is missing. Reload the confirmation page and try again.")
		return
	}
	if err := s.api.DeleteModel(r.Context(), id, etag); err != nil {
		data := s.basePage(session, "Delete model", "models")
		data.Confirm = &confirmView{Kind: "model", Name: id, Action: "/models/" + id + "/delete", Cancel: "/models", Warning: "The model was not deleted.", ETag: etag}
		s.renderOperationError(w, session, data, "confirm-delete", err)
		return
	}
	s.flashRedirect(w, r, session, "/models", "success", "Model deleted.")
}

func (s *server) keyReferenceData(ctx *http.Request, data *pageData) {
	var users []controlplane.User
	var models []controlplane.Model
	var providers []controlplane.Provider
	var userErr, modelErr, providerErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		users, userErr = s.api.ListUsers(ctx.Context())
	}()
	go func() {
		defer wait.Done()
		models, modelErr = s.api.ListModels(ctx.Context())
	}()
	go func() {
		defer wait.Done()
		providers, providerErr = s.api.ListProviders(ctx.Context())
	}()
	wait.Wait()
	data.Users, data.Models, data.Providers = users, models, providers
	data.UserChoicesAvailable, data.ModelChoicesAvailable = userErr == nil, modelErr == nil
	data.UserNames = make(map[string]string, len(users))
	for _, user := range users {
		data.UserNames[user.ID] = user.Name
	}
	if userErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("User choices are unavailable", userErr))
	}
	data.ModelNames = make(map[string]string, len(models))
	for _, model := range models {
		data.ModelNames[model.ID] = model.Alias
	}
	data.ProviderNames = make(map[string]string, len(providers))
	for _, provider := range providers {
		data.ProviderNames[provider.ID] = provider.Name
	}
	if modelErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Model choices are unavailable", modelErr))
	}
	if providerErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Provider names are unavailable", providerErr))
	}
	slices.SortFunc(data.Users, func(left, right controlplane.User) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	slices.SortFunc(data.Providers, func(left, right controlplane.Provider) int { return strings.Compare(left.Slug, right.Slug) })
	slices.SortFunc(data.Models, func(left, right controlplane.Model) int {
		return cmp.Or(strings.Compare(strings.ToLower(left.Alias), strings.ToLower(right.Alias)), strings.Compare(left.ID, right.ID))
	})
}

func (s *server) listVirtualKeys(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Virtual keys", "virtual-keys")
	var keys []controlplane.PublicVirtualKey
	var users []controlplane.User
	var models []controlplane.Model
	var err, userErr, modelErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		keys, err = s.api.ListVirtualKeys(r.Context())
	}()
	go func() {
		defer wait.Done()
		users, userErr = s.api.ListUsers(r.Context())
	}()
	go func() {
		defer wait.Done()
		models, modelErr = s.api.ListModels(r.Context())
	}()
	wait.Wait()
	if err != nil {
		s.renderOperationError(w, session, data, "virtual-keys", err)
		return
	}
	data.UserNames = make(map[string]string)
	for _, user := range users {
		data.UserNames[user.ID] = user.Name
	}
	data.Users = users
	slices.SortFunc(data.Users, func(left, right controlplane.User) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	if userErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("User names are unavailable", userErr))
	}
	data.ModelNames = make(map[string]string, len(models))
	for _, model := range models {
		data.ModelNames[model.ID] = model.Alias
	}
	data.Models = models
	if modelErr != nil {
		data.Warnings = append(data.Warnings, warningMessage("Model names are unavailable", modelErr))
	}
	data.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	query := strings.ToLower(data.Query)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	data.StatusFilter, data.UserFilter, data.ModelFilter = status, userID, modelID
	for _, key := range keys {
		modelSearch := ""
		for _, id := range key.ModelIDs {
			modelSearch += " " + id + " " + data.ModelNames[id]
		}
		if query != "" && !strings.Contains(strings.ToLower(key.Name+" "+key.Prefix+" "+key.ID+" "+key.UserID+" "+data.UserNames[key.UserID]+modelSearch), query) {
			continue
		}
		if status != "" && string(key.Status) != status {
			continue
		}
		if userID != "" && key.UserID != userID {
			continue
		}
		if modelID != "" && !slices.Contains(key.ModelIDs, modelID) {
			continue
		}
		data.VirtualKeys = append(data.VirtualKeys, key)
	}
	slices.SortFunc(data.VirtualKeys, func(left, right controlplane.PublicVirtualKey) int {
		return cmp.Or(strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), strings.Compare(left.ID, right.ID))
	})
	s.views.render(w, http.StatusOK, "virtual-keys", data)
}

func (s *server) newVirtualKey(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	data := s.basePage(session, "Create virtual key", "virtual-keys")
	data.VirtualKeyForm = &virtualKeyForm{Status: string(controlplane.StatusActive)}
	operationToken, err := s.sessions.issueKeyCreationToken(session.ID)
	if err != nil {
		s.renderSimpleError(w, session, http.StatusServiceUnavailable, "Key creation unavailable", "A one-time key-creation form could not be prepared. Reload the page and try again.")
		return
	}
	data.OperationToken = operationToken
	s.keyReferenceData(r, &data)
	s.views.render(w, http.StatusOK, "virtual-key-form", data)
}

func (s *server) refreshKeyCreationForm(w http.ResponseWriter, r *http.Request, session sessionSnapshot, data *pageData) bool {
	operationToken, err := s.sessions.issueKeyCreationToken(session.ID)
	if err != nil {
		s.renderSimpleError(w, session, http.StatusServiceUnavailable, "Key creation unavailable", "A new one-time key-creation form could not be prepared. Reload the page and try again.")
		return false
	}
	data.OperationToken = operationToken
	s.keyReferenceData(r, data)
	return true
}

func virtualKeyFormFromRequest(r *http.Request) virtualKeyForm {
	modelIDs := make([]string, 0, len(r.PostForm["model_ids"]))
	for _, modelID := range r.PostForm["model_ids"] {
		modelID = strings.TrimSpace(modelID)
		if modelID != "" && !slices.Contains(modelIDs, modelID) {
			modelIDs = append(modelIDs, modelID)
		}
	}
	return virtualKeyForm{
		Name: formString(r, "name"), UserID: formString(r, "user_id"),
		ModelIDs: modelIDs, Status: statusValue(r.PostForm.Get("status")),
		ExpiresAt: formString(r, "expires_at"), OriginalExpiresAt: formString(r, "expires_at_original"),
	}
}

func (s *server) createVirtualKey(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	form := virtualKeyFormFromRequest(r)
	form.OriginalExpiresAt = ""
	if !s.sessions.consumeKeyCreationToken(session.ID, r.PostForm.Get("_operation")) {
		data := s.basePage(session, "Create virtual key", "virtual-keys")
		data.VirtualKeyForm = &form
		if !s.refreshKeyCreationForm(w, r, session, &data) {
			return
		}
		data.Error = &viewError{Title: "Form already used", Detail: "This key-creation form was already submitted or expired. Review the current key list before trying again."}
		data.StatusCode = http.StatusConflict
		s.views.render(w, http.StatusConflict, "virtual-key-form", data)
		return
	}
	if !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Create virtual key", "virtual-keys")
		data.VirtualKeyForm = &form
		if !s.refreshKeyCreationForm(w, r, session, &data) {
			return
		}
		s.renderInvalidForm(w, data, "virtual-key-form", "Status must be active or disabled.")
		return
	}
	input, parseErr := form.input()
	if parseErr != nil {
		data := s.basePage(session, "Create virtual key", "virtual-keys")
		data.VirtualKeyForm = &form
		if !s.refreshKeyCreationForm(w, r, session, &data) {
			return
		}
		data.Error, data.StatusCode = &viewError{Title: "Invalid form", Detail: parseErr.Error()}, http.StatusBadRequest
		s.views.render(w, data.StatusCode, "virtual-key-form", data)
		return
	}
	created, err := s.api.CreateVirtualKey(r.Context(), input)
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Ambiguous {
			data := s.basePage(session, "Key creation outcome unknown", "virtual-keys")
			s.renderOperationError(w, session, data, "error", &APIError{
				Status: http.StatusBadGateway, Title: "Key creation outcome unknown",
				Detail:   "The control plane may have created the key, but its one-time secret could not be delivered safely. Inspect the virtual-key list and delete any matching key before deliberately creating a replacement.",
				Instance: apiError.Instance,
			})
			return
		}
		data := s.basePage(session, "Create virtual key", "virtual-keys")
		data.VirtualKeyForm = &form
		if !s.refreshKeyCreationForm(w, r, session, &data) {
			return
		}
		s.renderOperationError(w, session, data, "virtual-key-form", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	data := s.basePage(session, "Save virtual key", "virtual-keys")
	data.CreatedKey = &created
	s.views.render(w, http.StatusCreated, "key-reveal", data)
}

func (s *server) editVirtualKey(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetVirtualKey(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Edit virtual key", "virtual-keys")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	key := versioned.Value
	form := virtualKeyFormFromModel(key)
	data.VirtualKeyForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, key.ID, versioned.ETag
	s.keyReferenceData(r, &data)
	s.views.render(w, http.StatusOK, "virtual-key-form", data)
}

func (s *server) updateVirtualKey(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id := r.PathValue("id")
	etag := formString(r, "_etag")
	form := virtualKeyFormFromRequest(r)
	if etag == "" || !validLifecycleStatus(form.Status) {
		data := s.basePage(session, "Edit virtual key", "virtual-keys")
		data.VirtualKeyForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.keyReferenceData(r, &data)
		s.renderInvalidForm(w, data, "virtual-key-form", "A current resource version and an active or disabled status are required. Reload the edit page and try again.")
		return
	}
	input, parseErr := form.input()
	if parseErr != nil {
		data := s.basePage(session, "Edit virtual key", "virtual-keys")
		data.VirtualKeyForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.keyReferenceData(r, &data)
		data.Error, data.StatusCode = &viewError{Title: "Invalid form", Detail: parseErr.Error()}, http.StatusBadRequest
		s.views.render(w, data.StatusCode, "virtual-key-form", data)
		return
	}
	if _, err := s.api.UpdateVirtualKey(r.Context(), id, input, etag); err != nil {
		data := s.basePage(session, "Edit virtual key", "virtual-keys")
		data.VirtualKeyForm, data.Editing, data.ResourceID, data.ResourceETag = &form, true, id, etag
		s.keyReferenceData(r, &data)
		s.renderOperationError(w, session, data, "virtual-key-form", err)
		return
	}
	s.flashRedirect(w, r, session, "/virtual-keys", "success", "Virtual key updated.")
}

func (s *server) confirmVirtualKeyStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	status := strings.TrimSpace(r.URL.Query().Get("to"))
	if !validLifecycleStatus(status) {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "Status must be active or disabled.")
		return
	}
	versioned, err := s.api.GetVirtualKey(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Change virtual-key status", "virtual-keys")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	warning := "Enabling the key restores authorization only when its owner, selected models and provider routes are active and it has not expired."
	if status == string(controlplane.StatusDisabled) {
		warning = "Disabling the key revokes this credential immediately."
	}
	data.Lifecycle = &lifecycleView{
		Kind: "virtual key", Name: versioned.Value.Name, Status: status,
		Action: "/virtual-keys/" + versioned.Value.ID + "/status", Cancel: "/virtual-keys",
		Warning: warning, ButtonText: strings.ToUpper(status[:1]) + status[1:] + " key", ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-status", data)
}

func (s *server) changeVirtualKeyStatus(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	status, etag := formString(r, "status"), formString(r, "_etag")
	if !validLifecycleStatus(status) || etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid status", "A current resource version and an active or disabled status are required. Reload the confirmation page and try again.")
		return
	}
	id := r.PathValue("id")
	versioned, err := s.api.GetVirtualKey(r.Context(), id)
	if err == nil {
		key := versioned.Value
		input := controlplane.VirtualKeyInput{
			Name: key.Name, UserID: key.UserID, ModelIDs: slices.Clone(key.ModelIDs),
			Status: controlplane.Status(status), ExpiresAt: key.ExpiresAt,
		}
		_, err = s.api.UpdateVirtualKey(r.Context(), id, input, etag)
	}
	if err != nil {
		data := s.basePage(session, "Change key status", "virtual-keys")
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	message := "Virtual key enabled."
	if status == string(controlplane.StatusDisabled) {
		message = "Virtual key disabled and revoked."
	}
	s.flashRedirect(w, r, session, "/virtual-keys", "success", message)
}

func (s *server) confirmDeleteVirtualKey(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	versioned, err := s.api.GetVirtualKey(r.Context(), r.PathValue("id"))
	data := s.basePage(session, "Delete virtual key", "virtual-keys")
	if err != nil {
		s.renderOperationError(w, session, data, "error", err)
		return
	}
	key := versioned.Value
	data.Confirm = &confirmView{
		Kind: "virtual key", Name: key.Name, Action: "/virtual-keys/" + key.ID + "/delete",
		Cancel: "/virtual-keys", Warning: "The key will stop authorizing immediately. Its plaintext secret cannot be recovered.", ETag: versioned.ETag,
	}
	s.views.render(w, http.StatusOK, "confirm-delete", data)
}

func (s *server) deleteVirtualKey(w http.ResponseWriter, r *http.Request, session sessionSnapshot) {
	if !s.parseProtectedForm(w, r, session) {
		return
	}
	id := r.PathValue("id")
	etag := formString(r, "_etag")
	if etag == "" {
		s.renderSimpleError(w, session, http.StatusBadRequest, "Invalid deletion", "The resource version is missing. Reload the confirmation page and try again.")
		return
	}
	if err := s.api.DeleteVirtualKey(r.Context(), id, etag); err != nil {
		data := s.basePage(session, "Delete virtual key", "virtual-keys")
		data.Confirm = &confirmView{Kind: "virtual key", Name: id, Action: "/virtual-keys/" + id + "/delete", Cancel: "/virtual-keys", Warning: "The key was not deleted.", ETag: etag}
		s.renderOperationError(w, session, data, "confirm-delete", err)
		return
	}
	s.flashRedirect(w, r, session, "/virtual-keys", "success", "Virtual key deleted.")
}
