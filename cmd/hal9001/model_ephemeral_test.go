package main

import (
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
)

// The remove picker must never offer the flag/runtime override as a
// stored pair, and removing the phantom must stay refused: the stored
// configuration is the authority, not the listing.
func TestFlagEntryIsMarkedEphemeral(t *testing.T) {
	a := newEndpointApp(t)
	a.stored.Endpoints = []config.EndpointRecord{
		{ID: "deepseek-flash", Name: "DeepSeek", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "deepseek-flash"}}},
		{ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "MiniMax-M3.1-Flash"}}},
	}
	a.stored.SetActive("deepseek-flash", "deepseek-flash")
	a.cfg.Model = "MiniMax-M3.1-Flash"
	a.model = "MiniMax-M3.1-Flash"

	var flagged int
	for _, m := range a.listModels() {
		if !m.Ephemeral {
			continue
		}
		flagged++
		if m.Endpoint != "deepseek-flash" || m.ID != "MiniMax-M3.1-Flash" {
			t.Errorf("ephemeral entry = %v/%v, want deepseek-flash/MiniMax-M3.1-Flash", m.Endpoint, m.ID)
		}
		if !m.Active {
			t.Errorf("the effective model lost its active mark: %+v", m)
		}
		if !strings.Contains(m.Label, "(flags)") {
			t.Errorf("the flag-sourced entry lost its designation: %q", m.Label)
		}
	}
	if flagged != 1 {
		t.Fatalf("found %d ephemeral entries, want exactly 1", flagged)
	}
	for _, m := range a.listModels() {
		if m.Ephemeral {
			continue
		}
		if endpoint, ok := a.stored.EndpointByID(m.Endpoint); !ok {
			t.Errorf("stored entry names an unknown endpoint: %+v", m)
		} else if _, ok := endpoint.Model(m.ID); !ok {
			t.Errorf("stored entry names an unstored model: %+v", m)
		}
	}
}

// An environment override resolves the active mark: a stored row
// under a different endpoint must not look active just because its
// name matches the override.
func TestEnvOverrideDoesNotFalselyMarkStoredRows(t *testing.T) {
	a := newEndpointApp(t)
	a.stored.Endpoints = []config.EndpointRecord{
		{ID: "deepseek-flash", Name: "DeepSeek", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "deepseek-flash"}}},
		{ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "MiniMax-M3.1-Flash"}}},
	}
	a.stored.SetActive("deepseek-flash", "deepseek-flash")
	// Pure environment override: no flag value, no runtime override, so
	// no ephemeral row may be fabricated at all.
	a.cfg.Model = ""
	a.model = ""
	t.Setenv("HAL9001_MODEL", "MiniMax-M3.1-Flash")
	for _, m := range a.listModels() {
		if m.Active {
			t.Errorf("no stored row may look active under an env override: %+v", m)
		}
		if m.Ephemeral {
			t.Errorf("an env-only override must not fabricate a row: %+v", m)
		}
	}
}

// An override naming a stored model under the active endpoint keeps
// the single legitimate record: no duplicate, still actionable.
func TestOverrideMatchingStoredRecordKeepsOneActionableRow(t *testing.T) {
	a := newEndpointApp(t)
	a.stored.Endpoints = []config.EndpointRecord{
		{ID: "deepseek-flash", Name: "DeepSeek", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "deepseek-flash"}}},
	}
	a.stored.SetActive("deepseek-flash", "deepseek-flash")
	a.cfg.Model = "deepseek-flash"
	a.model = "deepseek-flash"
	rows := a.listModels()
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want exactly the stored pair: %+v", len(rows), rows)
	}
	if rows[0].Ephemeral {
		t.Errorf("the stored record was shadowed by a phantom: %+v", rows[0])
	}
	if !rows[0].Active {
		t.Errorf("the stored active pair lost its mark: %+v", rows[0])
	}
}

// Removing the phantom pair is refused with the reported error, and the
// stored configuration and the active selection are untouched.
func TestRemovePhantomPairIsRefusedWithoutMutation(t *testing.T) {
	a := newEndpointApp(t)
	a.stored.Endpoints = []config.EndpointRecord{
		{ID: "deepseek-flash", Name: "DeepSeek", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "deepseek-flash"}}},
		{ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "MiniMax-M3.1-Flash"}}},
	}
	a.stored.SetActive("deepseek-flash", "deepseek-flash")

	err := a.deleteModel("deepseek-flash", "MiniMax-M3.1-Flash", "")
	if err == nil || err.Error() != `endpoint "deepseek-flash" has no model "MiniMax-M3.1-Flash"` {
		t.Fatalf("error = %v, want the reported refusal", err)
	}
	endpoint, _ := a.stored.EndpointByID("deepseek-flash")
	if len(endpoint.Models) != 1 || endpoint.Models[0].ID != "deepseek-flash" {
		t.Errorf("the refusal mutated deepseek-flash: %+v", endpoint.Models)
	}
	if got := a.stored.ActiveEndpointID(); got != "deepseek-flash" {
		t.Errorf("the active endpoint moved to %q", got)
	}
	if got := a.stored.ActiveModelID(); got != "deepseek-flash" {
		t.Errorf("the active model moved to %q", got)
	}
}

// Removing a real stored pair deletes exactly that pair.
func TestRemoveRealPairKeepsTheOtherEndpoint(t *testing.T) {
	a := newEndpointApp(t)
	a.stored.Endpoints = []config.EndpointRecord{
		{ID: "deepseek-flash", Name: "DeepSeek", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "deepseek-flash"}}},
		{ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
			BaseURL: "http://127.0.0.1:9/v1",
			Models:  []config.ModelRecord{{ID: "MiniMax-M3.1-Flash"}}},
	}
	a.stored.SetActive("deepseek-flash", "deepseek-flash")

	if err := a.deleteModel("minimax", "MiniMax-M3.1-Flash", ""); err != nil {
		t.Fatalf("removing the stored pair failed: %v", err)
	}
	if endpoint, _ := a.stored.EndpointByID("minimax"); len(endpoint.Models) != 0 {
		t.Errorf("minimax still holds models: %+v", endpoint.Models)
	}
	if endpoint, _ := a.stored.EndpointByID("deepseek-flash"); len(endpoint.Models) != 1 {
		t.Errorf("deepseek-flash lost its model: %+v", endpoint.Models)
	}
	if got := a.stored.ActiveEndpointID(); got != "deepseek-flash" {
		t.Errorf("the active endpoint moved to %q", got)
	}
}
