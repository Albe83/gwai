package adminui

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
)

//go:embed templates/*.html assets/*
var webFiles embed.FS

type viewError struct {
	Title     string
	Detail    string
	RequestID string
}

type dashboardSection struct {
	Available bool
	Count     int
	Error     string
}

type pageData struct {
	Title          string
	Section        string
	Authenticated  bool
	CSRFToken      string
	Flashes        []flashMessage
	Error          *viewError
	StatusCode     int
	Warnings       []string
	Query          string
	StatusFilter   string
	UserFilter     string
	ProviderFilter string
	ModelFilter    string

	ResourceSection dashboardSection
	KeySection      dashboardSection

	Users                 []controlplane.User
	Providers             []controlplane.Provider
	Models                []controlplane.Model
	VirtualKeys           []controlplane.PublicVirtualKey
	UserKeyCounts         map[string]int
	UserNames             map[string]string
	ProviderModelCounts   map[string]int
	ProviderNames         map[string]string
	ModelKeyCounts        map[string]int
	ModelNames            map[string]string
	RelationsAvailable    bool
	UserChoicesAvailable  bool
	ModelChoicesAvailable bool

	UserForm       *userForm
	ProviderForm   *providerForm
	ModelForm      *modelForm
	VirtualKeyForm *virtualKeyForm
	Editing        bool
	ResourceID     string
	ResourceETag   string
	OperationToken string
	CreatedKey     *controlplane.CreatedVirtualKey
	Confirm        *confirmView
	Lifecycle      *lifecycleView
}

type confirmView struct {
	Kind    string
	Name    string
	Action  string
	Cancel  string
	Warning string
	ETag    string
}

type lifecycleView struct {
	Kind       string
	Name       string
	Status     string
	Action     string
	Cancel     string
	Warning    string
	ButtonText string
	ETag       string
}

type renderer struct {
	templates *template.Template
}

func newRenderer() (*renderer, error) {
	functions := template.FuncMap{
		"formatTime": func(value any) string {
			var timestamp time.Time
			switch typed := value.(type) {
			case time.Time:
				timestamp = typed
			case *time.Time:
				if typed != nil {
					timestamp = *typed
				}
			}
			if timestamp.IsZero() {
				return "—"
			}
			return timestamp.UTC().Format("2006-01-02 15:04 UTC")
		},
		"statusClass": func(status controlplane.Status) string {
			if status == controlplane.StatusActive {
				return "status status-active"
			}
			return "status status-disabled"
		},
		"hasString": func(values []string, value string) bool {
			return slices.Contains(values, value)
		},
	}
	parsed, err := template.New("admin").Funcs(functions).ParseFS(webFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin UI templates: %w", err)
	}
	return &renderer{templates: parsed}, nil
}

func (r *renderer) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := r.templates.ExecuteTemplate(w, name, data); err != nil {
		// Headers may already be committed; avoid exposing template internals.
		_, _ = fmt.Fprint(w, "\nUnable to render the administrative page.")
	}
}

func staticFiles() (fs.FS, error) {
	return fs.Sub(webFiles, "assets")
}

func errorView(err error) (*viewError, int) {
	if err == nil {
		return nil, http.StatusOK
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		status := apiError.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return &viewError{Title: apiError.Title, Detail: apiError.Detail, RequestID: apiError.Instance}, status
	}
	return &viewError{
		Title:  "Administrative operation failed",
		Detail: "The operation could not be completed. Try again or inspect the service logs.",
	}, http.StatusInternalServerError
}

type userForm struct {
	Name   string
	Email  string
	Status string
}

func userFormFromModel(user controlplane.User) userForm {
	return userForm{Name: user.Name, Email: user.Email, Status: string(user.Status)}
}

func (f userForm) input() controlplane.UserInput {
	return controlplane.UserInput{Name: f.Name, Email: f.Email, Status: controlplane.Status(f.Status)}
}

type providerForm struct {
	Slug            string
	Name            string
	Kind            string
	BaseURL         string
	APIVersion      string
	AdapterAppID    string
	SecretStore     string
	SecretName      string
	SecretKey       string
	SecretNamespace string
	Status          string
}

func providerFormFromModel(provider controlplane.Provider) providerForm {
	return providerForm{
		Slug: provider.Slug, Name: provider.Name, Kind: provider.Kind,
		BaseURL: provider.BaseURL, APIVersion: provider.APIVersion,
		AdapterAppID: provider.AdapterAppID,
		SecretStore:  provider.SecretRef.Store, SecretName: provider.SecretRef.Name,
		SecretKey: provider.SecretRef.Key, SecretNamespace: provider.SecretRef.Namespace,
		Status: string(provider.Status),
	}
}

func (f providerForm) input() controlplane.ProviderInput {
	return controlplane.ProviderInput{
		Slug: f.Slug, Name: f.Name, Kind: f.Kind, BaseURL: f.BaseURL,
		APIVersion: f.APIVersion, AdapterAppID: f.AdapterAppID,
		SecretRef: daprhttp.SecretRef{
			Store: f.SecretStore, Name: f.SecretName,
			Key: f.SecretKey, Namespace: f.SecretNamespace,
		},
		Status: controlplane.Status(f.Status),
	}
}

type modelForm struct {
	Alias         string
	ProviderID    string
	UpstreamModel string
	Status        string
}

func modelFormFromModel(model controlplane.Model) modelForm {
	return modelForm{
		Alias: model.Alias, ProviderID: model.ProviderID,
		UpstreamModel: model.UpstreamModel, Status: string(model.Status),
	}
}

func (f modelForm) input() controlplane.ModelInput {
	return controlplane.ModelInput{
		Alias: f.Alias, ProviderID: f.ProviderID,
		UpstreamModel: f.UpstreamModel, Status: controlplane.Status(f.Status),
	}
}

type virtualKeyForm struct {
	Name      string
	UserID    string
	ModelIDs  []string
	Status    string
	ExpiresAt string
}

func virtualKeyFormFromModel(key controlplane.PublicVirtualKey) virtualKeyForm {
	expires := ""
	if key.ExpiresAt != nil {
		expires = key.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999")
	}
	return virtualKeyForm{
		Name: key.Name, UserID: key.UserID,
		ModelIDs: slices.Clone(key.ModelIDs), Status: string(key.Status), ExpiresAt: expires,
	}
}

func (f virtualKeyForm) input() (controlplane.VirtualKeyInput, error) {
	modelIDs := make([]string, 0, len(f.ModelIDs))
	for _, modelID := range f.ModelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID != "" && !slices.Contains(modelIDs, modelID) {
			modelIDs = append(modelIDs, modelID)
		}
	}
	if len(modelIDs) == 0 {
		return controlplane.VirtualKeyInput{}, errors.New("at least one model must be selected")
	}
	slices.Sort(modelIDs)
	var expires *time.Time
	if strings.TrimSpace(f.ExpiresAt) != "" {
		parsed, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(f.ExpiresAt), time.UTC)
		if err != nil {
			return controlplane.VirtualKeyInput{}, fmt.Errorf("expiration must be a valid UTC date and time")
		}
		expires = &parsed
	}
	return controlplane.VirtualKeyInput{
		Name: f.Name, UserID: f.UserID, ModelIDs: modelIDs,
		Status: controlplane.Status(f.Status), ExpiresAt: expires,
	}, nil
}
