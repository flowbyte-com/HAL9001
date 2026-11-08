package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ConfigVersion is the version of the configuration schema HAL9001 writes.
//
// Version 1 is the original flat file: one provider, one endpoint, one
// credential and a list of model identifiers underneath them. Version 2
// replaces that with provider-scoped endpoints.
const ConfigVersion = 2

// BackupSuffix is appended to the configuration path for the copy of the
// file as it was before the first migration.
const BackupSuffix = ".v1.bak"

// envReference matches an environment-variable reference in a credential
// field: "$NAME" or "${NAME}".
var envReference = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)

// MigrationReport describes what a migration did. It never contains a
// credential: a report is shown to the operator and may be logged.
type MigrationReport struct {
	// Migrated reports whether the file was rewritten.
	Migrated bool
	// BackupPath is the copy of the file as it was before the rewrite.
	BackupPath string
	// EndpointID is the endpoint the legacy configuration became.
	EndpointID string
	// EndpointName is that endpoint's display name.
	EndpointName string
	// Models is the number of models the endpoint carries.
	Models int
	// Reviews are the models that need the operator's attention.
	Reviews []ReviewItem
	// Notes describe what happened, in one line each.
	Notes []string
}

// Summary returns a one-line summary of the migration.
func (r MigrationReport) Summary() string {
	if !r.Migrated {
		return "the configuration is already up to date"
	}
	return fmt.Sprintf("migrated the configuration: endpoint %q with %d model(s)", r.EndpointID, r.Models)
}

// Migrate upgrades a pre-version-2 configuration file to version 2.
//
// The migration is idempotent: a file that already carries the current
// version is left untouched. It is atomic: the new file is written beside
// the old one and renamed over it, so a reader sees either the old file
// or the new one and never a fragment. It is recoverable: the original
// file is copied next to itself before the first write.
//
// No credential ever appears in the report.
func Migrate(path string) (MigrationReport, error) {
	var report MigrationReport
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		report.Notes = append(report.Notes, "there is no configuration file yet")
		return report, nil
	}
	if err != nil {
		return report, fmt.Errorf("config: %w", err)
	}
	var fc FileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return report, fmt.Errorf("config: the configuration file is not valid JSON: %w", err)
	}
	if fc.Version >= ConfigVersion {
		report.Notes = append(report.Notes, fmt.Sprintf("the configuration is already version %d", fc.Version))
		return report, nil
	}
	if strings.TrimSpace(fc.Endpoint) == "" && len(fc.Models) == 0 {
		report.Notes = append(report.Notes, "there is nothing to migrate: no endpoint and no models are configured")
		return report, nil
	}

	backup := path + BackupSuffix
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := writeFileAtomic(backup, data, 0o600); err != nil {
			return report, fmt.Errorf("config: cannot write the migration backup: %w", err)
		}
		report.BackupPath = backup
		report.Notes = append(report.Notes, "the previous configuration was kept at "+filepath.Base(backup))
	}

	endpoint := migrateEndpoint(fc)
	activeModel, endpoint := migrateActiveModel(fc, endpoint)
	fc.Version = ConfigVersion
	fc.Endpoints = []EndpointRecord{endpoint}
	fc.Selection = &ActiveSelection{Endpoint: endpoint.ID, Model: activeModel}
	fc.Provider = ""
	fc.Endpoint = ""
	fc.APIKey = ""
	fc.Models = nil
	fc.ActiveModel = ""
	fc.SyncLegacyMirror()

	if err := SaveFile(path, fc); err != nil {
		return report, err
	}
	report.Migrated = true
	report.EndpointID = endpoint.ID
	report.EndpointName = endpoint.Display()
	report.Models = len(endpoint.Models)
	for _, m := range endpoint.Models {
		if strings.TrimSpace(m.Review) == "" {
			continue
		}
		report.Reviews = append(report.Reviews, ReviewItem{Endpoint: endpoint.ID, Model: m.ID, Note: m.Review})
	}
	report.Notes = append(report.Notes,
		fmt.Sprintf("the credential was kept as %s", credentialKind(endpoint)))
	return report, nil
}

// migrateEndpoint builds the endpoint the legacy configuration becomes.
//
// There was exactly one endpoint in the legacy file, so there is exactly
// one endpoint afterwards. Every legacy model moves under it, because
// there was no other endpoint to move them to. A model that looks like it
// belongs to a different provider is flagged for review, never relocated.
func migrateEndpoint(fc FileConfig) EndpointRecord {
	baseURL := strings.TrimSpace(fc.Endpoint)
	vendor := endpointVendor(baseURL)
	rec := EndpointRecord{
		ID:       deriveEndpointID(baseURL, fc),
		Name:     vendorDisplay(vendor, baseURL),
		Protocol: migrateProtocol(fc.Provider),
		BaseURL:  baseURL,
	}
	if rec.Protocol == ProtocolCustom {
		rec.WireProtocol = ""
		rec.Review = "the recorded provider type is not one HAL9001 knows — set the protocol and its wire protocol"
	}
	switch ref := envReference.FindStringSubmatch(fc.APIKey); {
	case ref != nil:
		rec.CredentialEnv = ref[1]
	default:
		rec.Credential = fc.APIKey
	}
	rec.Discovery = DiscoveryRecord{Enabled: true}
	for _, m := range fc.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		rec.Models = append(rec.Models, ModelRecord{
			ID:          id,
			Label:       m.Label,
			Effort:      m.Effort,
			ContextMode: ContextAuto,
			Source:      SourceMigrated,
			Review:      ReviewNote(baseURL, id),
		})
	}
	return rec
}

// migrateActiveModel keeps the active model the legacy file named.
//
// An active model that is not in the list is kept: it was configured, so
// it is added and flagged rather than dropped.
func migrateActiveModel(fc FileConfig, endpoint EndpointRecord) (string, EndpointRecord) {
	active := strings.TrimSpace(fc.ActiveModel)
	if active == "" {
		if len(endpoint.Models) > 0 {
			return endpoint.Models[0].ID, endpoint
		}
		return "", endpoint
	}
	if _, ok := endpoint.Model(active); ok {
		return active, endpoint
	}
	note := ReviewNote(endpoint.BaseURL, active)
	if note == "" {
		note = "the active model was not listed among the configured models — verify it"
	}
	endpoint.Models = append(endpoint.Models, ModelRecord{
		ID:          active,
		ContextMode: ContextAuto,
		Source:      SourceMigrated,
		Review:      note,
	})
	return active, endpoint
}

// migrateProtocol maps the legacy provider name onto the protocol
// taxonomy. An unknown name becomes "custom", which must name its wire
// protocol before it can be used.
func migrateProtocol(provider string) Protocol {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "openai-compatible", "openai", "openai_compatible":
		return ProtocolOpenAI
	case "anthropic-compatible", "anthropic":
		return ProtocolAnthropic
	case "local", "ollama", "llamacpp", "llama.cpp", "vllm":
		return ProtocolLocal
	default:
		return ProtocolCustom
	}
}

// deriveEndpointID builds a stable identifier from the endpoint's host.
//
// It is derived once, at migration time, and never changes afterwards:
// the identifier is what every later command names the endpoint by.
func deriveEndpointID(baseURL string, fc FileConfig) string {
	candidate := "endpoint"
	if host := hostOf(baseURL); host != "" {
		candidate = sanitiseID(host)
	}
	if candidate == "" {
		candidate = "endpoint"
	}
	taken := map[string]bool{}
	for _, e := range fc.Endpoints {
		taken[e.ID] = true
	}
	id := candidate
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", candidate, i)
	}
	return id
}

// hostOf returns the host of a base URL without its port.
func hostOf(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// sanitiseID turns a host into an identifier HAL can use.
func sanitiseID(host string) string {
	labels := strings.Split(strings.ToLower(host), ".")
	name := labels[0]
	if len(labels) >= 2 {
		second := labels[len(labels)-2]
		switch second {
		case "com", "org", "net", "io", "ai", "co", "dev", "app", "cloud", "cn", "eu", "me":
			if len(labels) >= 3 {
				name = labels[len(labels)-3]
			} else {
				name = second
			}
		default:
			name = second
		}
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return ""
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-_")
	}
	return out
}

// vendorDisplay is the operator-facing name for a vendor.
func vendorDisplay(vendor, baseURL string) string {
	switch vendor {
	case "minimax":
		return "MiniMax"
	case "deepseek":
		return "DeepSeek"
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "google":
		return "Google"
	case "moonshot":
		return "Moonshot"
	case "local":
		return "Local"
	case "":
		return "Endpoint"
	}
	return strings.ToUpper(vendor[:1]) + vendor[1:]
}

// credentialKind describes a credential without revealing it.
func credentialKind(endpoint EndpointRecord) string {
	switch {
	case endpoint.CredentialEnv != "":
		return "an environment variable (" + endpoint.CredentialEnv + ")"
	case endpoint.Credential != "":
		return "a literal secret in the configuration file"
	default:
		return "unset"
	}
}

// SyncLegacyMirror writes the pre-version-2 fields so that an older
// HAL9001 build can still read the file.
//
// The mirror always describes the active endpoint: it is a compatibility
// view, never the source of truth once the file carries version 2.
//
// A version-2 file with no active endpoint clears the mirror. Leaving it
// would keep the address of a deleted endpoint in the file, and the next
// launch would resolve that address as the active one: a deleted
// endpoint would come back.
func (f *FileConfig) SyncLegacyMirror() {
	if f.Version < ConfigVersion {
		// A pre-version-2 file owns these fields itself. There is no
		// endpoint to mirror them from.
		return
	}
	endpoint, ok := f.ActiveEndpoint()
	if !ok {
		f.Provider = ""
		f.Endpoint = ""
		f.APIKey = ""
		f.Models = nil
		f.ActiveModel = ""
		return
	}
	f.Provider = string(endpoint.Protocol)
	f.Endpoint = endpoint.BaseURL
	f.APIKey = endpoint.legacyCredential()
	f.ActiveModel = f.ActiveModelID()
	f.Models = make([]ModelRecord, 0, len(endpoint.Models))
	for _, m := range endpoint.Models {
		f.Models = append(f.Models, ModelRecord{ID: m.ID, Label: m.Label, Effort: m.Effort})
	}
}

// legacyCredential renders the endpoint's credential in the pre-version-2
// shape: an environment reference stays a reference, a literal secret
// stays a literal secret.
func (e EndpointRecord) legacyCredential() string {
	if name := strings.TrimSpace(e.CredentialEnv); name != "" {
		return "$" + name
	}
	return e.Credential
}

// writeFileAtomic writes data to a path beside itself and renames it over
// the target. The file never exists in a half-written state.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
