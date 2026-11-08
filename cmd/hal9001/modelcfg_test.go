package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/tui"
)

// newEndpointApp builds an application with two configured endpoints and
// an isolated configuration file.
func newEndpointApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", path)
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{
		{
			ID:            "minimax",
			Name:          "MiniMax",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://api.minimax.io/v1",
			CredentialEnv: "MINIMAX_API_KEY",
			Models: []config.ModelRecord{
				{ID: "MiniMax-M3.1-Flash", Effort: "medium", ContextMode: config.ContextAuto},
				{ID: "deekseek-flash", ContextMode: config.ContextAuto},
			},
		},
		{
			ID:       "local",
			Name:     "Local",
			Protocol: config.ProtocolLocal,
			BaseURL:  "http://127.0.0.1:11434/v1",
			Models: []config.ModelRecord{
				{ID: "llama3.2", ContextMode: config.ContextAuto},
			},
		},
	}
	stored.SetActive("minimax", "MiniMax-M3.1-Flash")
	return &app{
		cfg: config.Config{
			Endpoint: "https://api.minimax.io/v1", APIKey: "k",
			Model: "MiniMax-M3.1-Flash", StoreDir: t.TempDir(),
			EndpointID: "minimax", WireProtocol: "openai-compatible",
		},
		stored:  stored,
		session: "test",
	}
}

// readStored reads the configuration file the application wrote.
func readStored(t *testing.T) config.FileConfig {
	t.Helper()
	fc, err := config.LoadFile(os.Getenv("HAL9001_CONFIG"))
	if err != nil {
		t.Fatalf("cannot read the configuration: %v", err)
	}
	return fc
}

func TestAddModelKeepsTheActiveModel(t *testing.T) {
	a := newEndpointApp(t)
	if err := a.addModel("local", tui.ModelSpec{ID: "qwen3"}); err != nil {
		t.Fatalf("add model: %v", err)
	}
	if a.stored.ActiveModelID() != "MiniMax-M3.1-Flash" {
		t.Errorf("the active model changed to %q", a.stored.ActiveModelID())
	}
	if a.stored.ActiveEndpointID() != "minimax" {
		t.Errorf("the active endpoint changed to %q", a.stored.ActiveEndpointID())
	}
	// The model was stored under the endpoint it was named for.
	local, _ := a.stored.EndpointByID("local")
	found := false
	for _, m := range local.Models {
		if m.ID == "qwen3" {
			found = true
		}
	}
	if !found {
		t.Errorf("the model was not stored under its endpoint: %+v", local.Models)
	}
	// The file on disk agrees with the running configuration.
	stored := readStored(t)
	local, _ = stored.EndpointByID("local")
	if len(local.Models) != 2 {
		t.Errorf("the file holds %d models under the local endpoint", len(local.Models))
	}
}

func TestAddModelRefusesADuplicateUnderTheSameEndpoint(t *testing.T) {
	a := newEndpointApp(t)
	err := a.addModel("minimax", tui.ModelSpec{ID: "MiniMax-M3.1-Flash"})
	if err == nil {
		t.Fatal("a duplicate model was accepted")
	}
	// The same identifier under another endpoint is a different model and
	// is accepted.
	if err := a.addModel("local", tui.ModelSpec{ID: "MiniMax-M3.1-Flash"}); err != nil {
		t.Fatalf("the same identifier under another endpoint was refused: %v", err)
	}
}

func TestAddModelRefusesAnUnknownEndpoint(t *testing.T) {
	a := newEndpointApp(t)
	if err := a.addModel("nowhere", tui.ModelSpec{ID: "m"}); err == nil {
		t.Fatal("a model was added to an endpoint that does not exist")
	}
}

func TestRemovingTheActiveModelNeedsAnExplicitReplacement(t *testing.T) {
	a := newEndpointApp(t)
	err := a.deleteModel("minimax", "MiniMax-M3.1-Flash", "")
	if err == nil {
		t.Fatal("the active model was removed without a replacement")
	}
	if !strings.Contains(err.Error(), "--use") {
		t.Errorf("the refusal does not tell the operator what to do: %v", err)
	}
	// Nothing changed: the model is still configured, and the refused
	// removal wrote nothing to disk.
	if _, ok := a.stored.ModelFor("minimax", "MiniMax-M3.1-Flash"); !ok {
		t.Error("the model was removed anyway")
	}
	if _, err := os.Stat(os.Getenv("HAL9001_CONFIG")); !os.IsNotExist(err) {
		t.Error("the refused removal wrote to the configuration file")
	}
}

func TestRemovingTheActiveModelWithAReplacement(t *testing.T) {
	a := newEndpointApp(t)
	if err := a.deleteModel("minimax", "MiniMax-M3.1-Flash", "local/llama3.2"); err != nil {
		t.Fatalf("remove model: %v", err)
	}
	if _, ok := a.stored.ModelFor("minimax", "MiniMax-M3.1-Flash"); ok {
		t.Error("the model was not removed")
	}
	if a.stored.ActiveEndpointID() != "local" || a.stored.ActiveModelID() != "llama3.2" {
		t.Errorf("the replacement was not activated: %+v", a.stored.Selection)
	}
	stored := readStored(t)
	if stored.ActiveEndpointID() != "local" || stored.ActiveModelID() != "llama3.2" {
		t.Errorf("the file holds the wrong selection: %+v", stored.Selection)
	}
}

func TestRemovingAModelThatIsNotActiveNeedsNoReplacement(t *testing.T) {
	a := newEndpointApp(t)
	if err := a.deleteModel("minimax", "deekseek-flash", ""); err != nil {
		t.Fatalf("remove model: %v", err)
	}
	if a.stored.ActiveModelID() != "MiniMax-M3.1-Flash" {
		t.Errorf("the active model changed to %q", a.stored.ActiveModelID())
	}
}

func TestRemovingTheEndpointOfTheActiveModelNeedsAReplacement(t *testing.T) {
	a := newEndpointApp(t)
	err := a.deleteEndpoint("minimax", "")
	if err == nil {
		t.Fatal("the endpoint of the active model was removed without a replacement")
	}
	if !strings.Contains(err.Error(), "--use") {
		t.Errorf("the refusal does not tell the operator what to do: %v", err)
	}
	err = a.deleteEndpoint("minimax", "local/llama3.2")
	if err != nil {
		t.Fatalf("remove endpoint: %v", err)
	}
	if _, ok := a.stored.EndpointByID("minimax"); ok {
		t.Error("the endpoint was not removed")
	}
	if a.stored.ActiveEndpointID() != "local" {
		t.Errorf("the replacement was not activated: %+v", a.stored.Selection)
	}
}

func TestRemovingAnEndpointThatHoldsNoActiveModelNeedsNoReplacement(t *testing.T) {
	a := newEndpointApp(t)
	if err := a.deleteEndpoint("local", ""); err != nil {
		t.Fatalf("remove endpoint: %v", err)
	}
	if a.stored.ActiveEndpointID() != "minimax" {
		t.Errorf("the active endpoint changed to %q", a.stored.ActiveEndpointID())
	}
}

func TestEditModelChangesOnlyTheNamedSettings(t *testing.T) {
	a := newEndpointApp(t)
	err := a.editModel("minimax", "MiniMax-M3.1-Flash", tui.ModelSpec{Effort: "high"})
	if err != nil {
		t.Fatalf("edit model: %v", err)
	}
	model, _ := a.stored.ModelFor("minimax", "MiniMax-M3.1-Flash")
	if model.Effort != "high" {
		t.Errorf("effort = %q", model.Effort)
	}
	if model.ContextMode != config.ContextAuto {
		t.Errorf("the context mode changed to %q", model.ContextMode)
	}
}

func TestModelContextConfiguresTheLimit(t *testing.T) {
	a := newEndpointApp(t)
	err := a.editModel("minimax", "MiniMax-M3.1-Flash", tui.ModelSpec{ContextMode: config.ContextManual, ContextWindow: 4096})
	if err != nil {
		t.Fatalf("edit model: %v", err)
	}
	model, _ := a.stored.ModelFor("minimax", "MiniMax-M3.1-Flash")
	if model.ContextMode != config.ContextManual || model.ContextWindow != 4096 {
		t.Errorf("the context settings are %+v", model)
	}
	// The limit reaches the provider request.
	a.rebuildEngine()
	if a.modelFor().ContextWindow != 4096 {
		t.Errorf("the model has no context window: %+v", a.modelFor())
	}
	// Automatic mode clears the explicit limit.
	err = a.editModel("minimax", "MiniMax-M3.1-Flash", tui.ModelSpec{ContextMode: config.ContextAuto})
	if err != nil {
		t.Fatalf("edit model: %v", err)
	}
	model, _ = a.stored.ModelFor("minimax", "MiniMax-M3.1-Flash")
	if model.ContextMode != config.ContextAuto || model.ContextWindow != 0 {
		t.Errorf("the context settings are %+v", model)
	}
}

func TestSwitchModelIsScopedToItsEndpoint(t *testing.T) {
	a := newEndpointApp(t)
	if err := a.addModel("local", tui.ModelSpec{ID: "MiniMax-M3.1-Flash"}); err != nil {
		t.Fatalf("add model: %v", err)
	}
	// The same identifier exists under both endpoints now.
	if _, err := a.switchModel("local", "MiniMax-M3.1-Flash"); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	if a.stored.ActiveEndpointID() != "local" {
		t.Errorf("the active endpoint = %q", a.stored.ActiveEndpointID())
	}
	// The selection is persisted.
	stored := readStored(t)
	if stored.ActiveEndpointID() != "local" {
		t.Errorf("the file holds the wrong endpoint: %+v", stored.Selection)
	}
}

func TestSwitchModelRefusesAModelOfAnotherEndpoint(t *testing.T) {
	a := newEndpointApp(t)
	if _, err := a.switchModel("local", "MiniMax-M3.1-Flash"); err == nil {
		t.Fatal("a model of another endpoint was activated")
	}
}

func TestSaveEndpointAddsAndUpdates(t *testing.T) {
	a := newEndpointApp(t)
	err := a.saveEndpoint(tui.EndpointSpec{
		ID: "deepseek", Name: "DeepSeek", Protocol: "openai-compatible",
		BaseURL: "https://api.deepseek.com/v1", CredentialEnv: "DEEPSEEK_API_KEY",
	})
	if err != nil {
		t.Fatalf("save endpoint: %v", err)
	}
	endpoint, ok := a.stored.EndpointByID("deepseek")
	if !ok {
		t.Fatal("the endpoint was not stored")
	}
	if endpoint.CredentialEnv != "DEEPSEEK_API_KEY" {
		t.Errorf("the credential reference = %q", endpoint.CredentialEnv)
	}
	// An update changes only what it names: the models survive.
	if err := a.addModel("deepseek", tui.ModelSpec{ID: "deepseek-chat"}); err != nil {
		t.Fatalf("add model: %v", err)
	}
	err = a.saveEndpoint(tui.EndpointSpec{ID: "deepseek", Name: "DeepSeek EU"})
	if err != nil {
		t.Fatalf("save endpoint: %v", err)
	}
	endpoint, _ = a.stored.EndpointByID("deepseek")
	if endpoint.Name != "DeepSeek EU" {
		t.Errorf("the name = %q", endpoint.Name)
	}
	if endpoint.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("the base url changed to %q", endpoint.BaseURL)
	}
	if len(endpoint.Models) != 1 {
		t.Errorf("the models were lost: %+v", endpoint.Models)
	}
}

func TestSaveEndpointRefusesACustomEndpointWithoutAWireProtocol(t *testing.T) {
	a := newEndpointApp(t)
	err := a.saveEndpoint(tui.EndpointSpec{
		ID: "odd", Name: "Odd", Protocol: "custom", BaseURL: "https://x/v1",
	})
	if err == nil {
		t.Fatal("a custom endpoint without a wire protocol was accepted")
	}
}

func TestEndpointInfosNeverCarryTheCredential(t *testing.T) {
	a := newEndpointApp(t)
	infos := a.endpointInfos()
	if len(infos) != 2 {
		t.Fatalf("endpoints = %d", len(infos))
	}
	for _, info := range infos {
		if info.Credential == "" {
			t.Errorf("endpoint %q has no credential description", info.ID)
		}
		if strings.Contains(info.Credential, "sk-") {
			t.Errorf("endpoint %q carries a secret: %q", info.ID, info.Credential)
		}
	}
	if infos[0].Credential != "env:MINIMAX_API_KEY" {
		t.Errorf("the credential description = %q", infos[0].Credential)
	}
}

func TestModelDetailsNeverCarryTheCredential(t *testing.T) {
	a := newEndpointApp(t)
	lines, err := a.modelDetails("minimax", "MiniMax-M3.1-Flash")
	if err != nil {
		t.Fatalf("model details: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "sk-") {
		t.Errorf("the details carry a secret:\n%s", joined)
	}
	if !strings.Contains(joined, "context") {
		t.Errorf("the details do not mention the context settings:\n%s", joined)
	}
}
