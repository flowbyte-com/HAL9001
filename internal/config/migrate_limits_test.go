package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The migration must not lose what the operator configured.
//
// A legacy file may carry a context window and an output cap. They are
// explicit numbers the operator wrote down, and the migration carries
// them over: a configured limit is never turned into an automatic mode
// and never becomes a zero.

// TestTheMigrationKeepsTheConfiguredLimits checks that a legacy record
// with a context window of 22585 and an output cap of 4096 keeps both
// numbers, and that the limit stays a limit the operator set.
func TestTheMigrationKeepsTheConfiguredLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := map[string]any{
		"provider":     "openai-compatible",
		"endpoint":     "https://api.example.invalid/v1",
		"api_key":      "$EXAMPLE_KEY",
		"active_model": "example-model",
		"models": []map[string]any{{
			"id":                "example-model",
			"context_window":    22585,
			"max_output_tokens": 4096,
		}},
	}
	original, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !report.Migrated {
		t.Fatal("the file was not migrated")
	}

	// The backup is byte-exact: the operator's own file is still there.
	backup, err := os.ReadFile(path + BackupSuffix)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if string(backup) != string(original) {
		t.Errorf("the backup is not byte-exact:\n%s", backup)
	}

	migrated, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	endpoints := migrated.Endpoints
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %+v", endpoints)
	}
	record, ok := endpoints[0].Model("example-model")
	if !ok {
		t.Fatalf("the model was not migrated: %+v", endpoints[0].Models)
	}
	if record.ContextWindow != 22585 {
		t.Errorf("the context window = %d, want 22585", record.ContextWindow)
	}
	if record.MaxOutputTokens != 4096 {
		t.Errorf("the output cap = %d, want 4096", record.MaxOutputTokens)
	}
	if record.ContextMode != ContextManual {
		t.Errorf("the mode = %q: an explicitly configured limit must not become automatic", record.ContextMode)
	}
	// The credential reference stays a reference.
	if endpoints[0].CredentialEnv != "EXAMPLE_KEY" {
		t.Errorf("the credential reference = %q", endpoints[0].CredentialEnv)
	}
	if endpoints[0].Credential != "" {
		t.Errorf("the resolved secret was written to the file: %q", endpoints[0].Credential)
	}
}

// TestTheMigrationIsIdempotent checks that a file that already carries
// the current schema is left alone, and that migrating it twice changes
// nothing.
func TestTheMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := map[string]any{
		"provider":     "openai-compatible",
		"endpoint":     "https://api.example.invalid/v1",
		"api_key":      "$EXAMPLE_KEY",
		"active_model": "example-model",
		"models": []map[string]any{{
			"id":                "example-model",
			"context_window":    22585,
			"max_output_tokens": 4096,
		}},
	}
	original, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(path)
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if report.Migrated {
		t.Error("the second migration rewrote the file")
	}
	afterSecond, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("the second migration changed the file:\n%s", afterSecond)
	}
}

// TestAMigratedRecordKeepsItsLimitsAcrossAWrite checks that the limits
// survive the round trip: they are written back into the file and read
// again unchanged.
func TestAMigratedRecordKeepsItsLimitsAcrossAWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	fc := FileConfig{
		Version: ConfigVersion,
		Endpoints: []EndpointRecord{{
			ID: "example", Name: "Example", Protocol: ProtocolOpenAI,
			BaseURL: "https://api.example.invalid/v1",
			Models: []ModelRecord{{
				ID: "example-model", ContextMode: ContextManual,
				ContextWindow: 22585, MaxOutputTokens: 4096,
			}},
		}},
	}
	fc.SetActive("example", "example-model")
	if err := SaveFile(path, fc); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, ok := reloaded.ActiveEndpoint()
	if !ok {
		t.Fatal("the active endpoint was lost")
	}
	record, ok := endpoint.Model("example-model")
	if !ok {
		t.Fatalf("the model was lost: %+v", endpoint.Models)
	}
	if record.ContextWindow != 22585 {
		t.Errorf("the context window = %d, want 22585", record.ContextWindow)
	}
	if record.MaxOutputTokens != 4096 {
		t.Errorf("the output cap = %d, want 4096", record.MaxOutputTokens)
	}
}
