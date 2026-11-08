package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes a configuration file for a test.
func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("cannot write the fixture: %v", err)
	}
	return path
}

// readConfig reads a configuration file back.
func readConfig(t *testing.T, path string) FileConfig {
	t.Helper()
	fc, err := LoadFile(path)
	if err != nil {
		t.Fatalf("cannot read the configuration: %v", err)
	}
	return fc
}

const operatorFixture = `{
  "provider": "openai-compatible",
  "endpoint": "https://api.minimax.io/v1",
  "api_key": "$MINIMAX_API_KEY",
  "models": [
    {"id": "MiniMax-M3.1-Flash", "effort": "medium"},
    {"id": "deekseek-flash"}
  ],
  "active_model": "deekseek-flash",
  "theme": "HAL Classic",
  "active_session": "cli-20261107-195136-3"
}`

func TestMigrateKeepsEndpointCredentialAndModels(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	report, err := Migrate(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !report.Migrated {
		t.Fatal("the configuration was not migrated")
	}
	fc := readConfig(t, path)
	if fc.Version != ConfigVersion {
		t.Fatalf("version = %d, want %d", fc.Version, ConfigVersion)
	}
	if len(fc.Endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(fc.Endpoints))
	}
	endpoint := fc.Endpoints[0]
	if endpoint.BaseURL != "https://api.minimax.io/v1" {
		t.Errorf("base url = %q", endpoint.BaseURL)
	}
	if endpoint.Protocol != ProtocolOpenAI {
		t.Errorf("protocol = %q", endpoint.Protocol)
	}
	// The environment reference stays a reference.
	if endpoint.CredentialEnv != "MINIMAX_API_KEY" {
		t.Errorf("credential env = %q, want MINIMAX_API_KEY", endpoint.CredentialEnv)
	}
	if endpoint.Credential != "" {
		t.Errorf("a literal credential was stored next to the reference: %q", endpoint.Credential)
	}
	if len(endpoint.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(endpoint.Models))
	}
	if endpoint.Models[0].ID != "MiniMax-M3.1-Flash" {
		t.Errorf("model 0 = %q", endpoint.Models[0].ID)
	}
	if endpoint.Models[0].Effort != "medium" {
		t.Errorf("the reasoning level was lost: %q", endpoint.Models[0].Effort)
	}
	if endpoint.Models[1].ID != "deekseek-flash" {
		t.Errorf("model 1 = %q", endpoint.Models[1].ID)
	}
	// The active model is the one the file named.
	if fc.ActiveModelID() != "deekseek-flash" {
		t.Errorf("active model = %q", fc.ActiveModelID())
	}
	if fc.ActiveEndpointID() != endpoint.ID {
		t.Errorf("active endpoint = %q", fc.ActiveEndpointID())
	}
	// The unrelated settings survive.
	if fc.Theme != "HAL Classic" {
		t.Errorf("theme = %q", fc.Theme)
	}
	if fc.ActiveSession != "cli-20261107-195136-3" {
		t.Errorf("active session = %q", fc.ActiveSession)
	}
}

func TestMigrateFlagsQuestionableModelForReview(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fc := readConfig(t, path)
	endpoint := fc.Endpoints[0]
	minimax, _ := endpoint.Model("MiniMax-M3.1-Flash")
	if minimax.Review != "" {
		t.Errorf("a matching model was flagged: %q", minimax.Review)
	}
	if minimax.Source != SourceMigrated {
		t.Errorf("source = %q", minimax.Source)
	}
	deepseek, _ := endpoint.Model("deekseek-flash")
	if deepseek.Review == "" {
		t.Fatal("the questionable model was not flagged for review")
	}
	if !strings.Contains(deepseek.Review, "deepseek") {
		t.Errorf("the review note does not name the likely model: %q", deepseek.Review)
	}
	// The flagged model stays under the endpoint it was configured
	// under: HAL never moves a model to a different provider.
	if len(fc.Endpoints) != 1 {
		t.Errorf("the model was moved to another endpoint: %d endpoints", len(fc.Endpoints))
	}
}

func TestMigrateKeepsLiteralCredentialVerbatim(t *testing.T) {
	path := writeFixture(t, `{"endpoint":"https://api.example.com/v1","api_key":"sk-secret-value","models":[{"id":"m1"}]}`)
	report, err := Migrate(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fc := readConfig(t, path)
	if fc.Endpoints[0].Credential != "sk-secret-value" {
		t.Errorf("the credential was not kept: %q", fc.Endpoints[0].Credential)
	}
	if fc.Endpoints[0].CredentialEnv != "" {
		t.Errorf("a literal credential became an environment reference: %q", fc.Endpoints[0].CredentialEnv)
	}
	// The report never carries the credential.
	if strings.Contains(report.Summary(), "sk-secret-value") {
		t.Error("the report contains the credential")
	}
	for _, note := range report.Notes {
		if strings.Contains(note, "sk-secret-value") {
			t.Errorf("a note contains the credential: %q", note)
		}
	}
	for _, review := range report.Reviews {
		if strings.Contains(review.Note, "sk-secret-value") {
			t.Errorf("a review note contains the credential: %q", review.Note)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	report, err := Migrate(path)
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if report.Migrated {
		t.Error("the second migration rewrote the file")
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the second migration changed the file")
	}
}

func TestMigrateKeepsABackupOfTheOriginal(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	backup := path + BackupSuffix
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("the backup is missing: %v", err)
	}
	if !strings.Contains(string(data), "deekseek-flash") {
		t.Error("the backup does not hold the original configuration")
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the backup mode is %v, want 0600", info.Mode().Perm())
	}
}

func TestMigrateRefusesMalformedJSONAndChangesNothing(t *testing.T) {
	path := writeFixture(t, `{"endpoint": "https://api.example.com/v1",`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := Migrate(path); err == nil {
		t.Fatal("a malformed configuration was migrated")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a failed migration changed the file")
	}
	if _, err := os.Stat(path + BackupSuffix); !os.IsNotExist(err) {
		t.Error("a failed migration left a backup behind")
	}
}

func TestMigratePreservesMCPAndUnrelatedFields(t *testing.T) {
	path := writeFixture(t, `{
      "endpoint": "https://api.example.com/v1",
      "api_key": "k",
      "models": [{"id": "m1"}],
      "mcp_servers": [{"name": "tools", "transport": "stdio", "command": "/bin/true"}],
      "max_tokens": 1234,
      "stream_idle_timeout_seconds": 11,
      "turn_timeout_seconds": 22,
      "theme": "Midnight",
      "active_session": "cli-1"
    }`)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fc := readConfig(t, path)
	if len(fc.MCPServers) != 1 || fc.MCPServers[0].Name != "tools" {
		t.Errorf("the MCP servers were lost: %+v", fc.MCPServers)
	}
	if fc.MaxTokens != 1234 || fc.StreamIdleSeconds != 11 || fc.TurnTimeoutSeconds != 22 {
		t.Errorf("the safeguards were lost: %+v", fc)
	}
	if fc.Theme != "Midnight" || fc.ActiveSession != "cli-1" {
		t.Errorf("the unrelated settings were lost: %+v", fc)
	}
}

func TestMigrateLeavesAVersion2FileAlone(t *testing.T) {
	body := `{"version":2,"endpoints":[{"id":"a","name":"A","protocol":"openai-compatible","base_url":"https://x/v1","models":[{"id":"m"}]}],"active":{"endpoint":"a","model":"m"}}`
	path := writeFixture(t, body)
	report, err := Migrate(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if report.Migrated {
		t.Error("a version-2 file was migrated again")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != body {
		t.Error("a version-2 file was rewritten")
	}
}

func TestMigrateKeepsTheLegacyMirrorReadableByAnOlderBuild(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fc := readConfig(t, path)
	if fc.Endpoint != "https://api.minimax.io/v1" {
		t.Errorf("the legacy endpoint mirror = %q", fc.Endpoint)
	}
	if fc.APIKey != "$MINIMAX_API_KEY" {
		t.Errorf("the legacy credential mirror = %q", fc.APIKey)
	}
	if fc.ActiveModel != "deekseek-flash" {
		t.Errorf("the legacy active model mirror = %q", fc.ActiveModel)
	}
	if len(fc.Models) != 2 {
		t.Errorf("the legacy model mirror = %+v", fc.Models)
	}
}

func TestMigratedFileIsValidJSONWithTheExpectedShape(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("the migrated file is not valid JSON: %v", err)
	}
	if generic["version"] != float64(ConfigVersion) {
		t.Errorf("version = %v", generic["version"])
	}
	endpoints, ok := generic["endpoints"].([]any)
	if !ok || len(endpoints) != 1 {
		t.Fatalf("endpoints = %v", generic["endpoints"])
	}
	endpoint := endpoints[0].(map[string]any)
	for _, key := range []string{"id", "name", "protocol", "base_url", "models"} {
		if _, ok := endpoint[key]; !ok {
			t.Errorf("the endpoint has no %q: %v", key, endpoint)
		}
	}
	active, ok := generic["active"].(map[string]any)
	if !ok {
		t.Fatalf("active = %v", generic["active"])
	}
	if active["endpoint"] != endpoint["id"] || active["model"] != "deekseek-flash" {
		t.Errorf("active = %v", active)
	}
}

func TestMigrateKeepsTheFileModeTight(t *testing.T) {
	path := writeFixture(t, operatorFixture)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the mode is %v, want 0600", info.Mode().Perm())
	}
}

func TestMigrateAddsAnActiveModelThatWasNotListed(t *testing.T) {
	path := writeFixture(t, `{"endpoint":"https://api.example.com/v1","api_key":"k","models":[{"id":"m1"}],"active_model":"ghost"}`)
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fc := readConfig(t, path)
	if fc.ActiveModelID() != "ghost" {
		t.Errorf("the active model = %q", fc.ActiveModelID())
	}
	model, ok := fc.ActiveModelRecord()
	if !ok {
		t.Fatal("the active model was dropped")
	}
	if model.Review == "" {
		t.Error("the unlisted active model was not flagged for review")
	}
}
