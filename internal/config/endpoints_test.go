package config

import (
	"os"
	"strings"
	"testing"
)

func testConfig() FileConfig {
	fc := FileConfig{Version: ConfigVersion}
	fc.Endpoints = []EndpointRecord{
		{
			ID:            "minimax",
			Name:          "MiniMax",
			Protocol:      ProtocolOpenAI,
			BaseURL:       "https://api.minimax.io/v1",
			CredentialEnv: "MINIMAX_API_KEY",
			Models: []ModelRecord{
				{ID: "MiniMax-M3.1-Flash", Effort: "medium", ContextMode: ContextAuto},
			},
		},
		{
			ID:       "local",
			Name:     "Local",
			Protocol: ProtocolLocal,
			BaseURL:  "http://127.0.0.1:11434/v1",
			Models: []ModelRecord{
				{ID: "llama3.2", ContextMode: ContextAuto},
			},
		},
	}
	fc.SetActive("minimax", "MiniMax-M3.1-Flash")
	return fc
}

func TestAddEndpointRefusesADuplicateIdentifier(t *testing.T) {
	fc := testConfig()
	err := fc.AddEndpoint(EndpointRecord{ID: "minimax", Name: "Other", Protocol: ProtocolOpenAI, BaseURL: "https://x/v1"})
	if err == nil {
		t.Fatal("a duplicate endpoint id was accepted")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if len(fc.Endpoints) != 2 {
		t.Errorf("endpoints = %d", len(fc.Endpoints))
	}
}

func TestAddEndpointRefusesAnUnusableRecord(t *testing.T) {
	fc := testConfig()
	cases := []struct {
		name string
		rec  EndpointRecord
	}{
		{"no id", EndpointRecord{Name: "X", Protocol: ProtocolOpenAI, BaseURL: "https://x/v1"}},
		{"bad id", EndpointRecord{ID: "Bad Id", Name: "X", Protocol: ProtocolOpenAI, BaseURL: "https://x/v1"}},
		{"no name", EndpointRecord{ID: "x", Protocol: ProtocolOpenAI, BaseURL: "https://x/v1"}},
		{"no url", EndpointRecord{ID: "x", Name: "X", Protocol: ProtocolOpenAI}},
		{"unknown protocol", EndpointRecord{ID: "x", Name: "X", Protocol: "gopher", BaseURL: "https://x/v1"}},
	}
	for _, tc := range cases {
		if err := fc.AddEndpoint(tc.rec); err == nil {
			t.Errorf("%s: the record was accepted", tc.name)
		}
	}
}

func TestCustomEndpointMustNameASupportedWireProtocol(t *testing.T) {
	fc := testConfig()
	err := fc.AddEndpoint(EndpointRecord{ID: "odd", Name: "Odd", Protocol: ProtocolCustom, BaseURL: "https://x/v1"})
	if err == nil {
		t.Fatal("a custom endpoint without a wire protocol was accepted")
	}
	if !strings.Contains(err.Error(), "wire protocol") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	// With a supported wire protocol it is accepted.
	err = fc.AddEndpoint(EndpointRecord{ID: "odd", Name: "Odd", Protocol: ProtocolCustom, WireProtocol: WireOpenAI, BaseURL: "https://x/v1"})
	if err != nil {
		t.Fatalf("a custom endpoint with a supported wire protocol was refused: %v", err)
	}
}

func TestCustomEndpointRefusesAnUnsupportedWireProtocol(t *testing.T) {
	rec := EndpointRecord{ID: "odd", Name: "Odd", Protocol: ProtocolCustom, WireProtocol: "grpc", BaseURL: "https://x/v1"}
	if err := rec.Validate(); err == nil {
		t.Fatal("an unsupported wire protocol was accepted")
	}
}

func TestModelIdentifiersStayScopedToTheirEndpoint(t *testing.T) {
	fc := testConfig()
	// The same identifier may exist under two endpoints: the two are
	// not the same model.
	if err := fc.AddModelTo("local", ModelRecord{ID: "MiniMax-M3.1-Flash"}); err != nil {
		t.Fatalf("the same identifier under a second endpoint was refused: %v", err)
	}
	// The same identifier twice under one endpoint is refused.
	if err := fc.AddModelTo("local", ModelRecord{ID: "llama3.2"}); err == nil {
		t.Fatal("a duplicate model under one endpoint was accepted")
	}
	local, _ := fc.EndpointByID("local")
	if len(local.Models) != 2 {
		t.Fatalf("the local endpoint has %d models", len(local.Models))
	}
	minimax, _ := fc.EndpointByID("minimax")
	if len(minimax.Models) != 1 {
		t.Fatalf("the minimax endpoint has %d models", len(minimax.Models))
	}
}

func TestRemoveModelAndEndpoint(t *testing.T) {
	fc := testConfig()
	if err := fc.RemoveModel("local", "llama3.2"); err != nil {
		t.Fatalf("remove model: %v", err)
	}
	local, _ := fc.EndpointByID("local")
	if len(local.Models) != 0 {
		t.Errorf("the model was not removed: %+v", local.Models)
	}
	if err := fc.RemoveModel("local", "llama3.2"); err == nil {
		t.Error("removing a model twice was accepted")
	}
	if err := fc.RemoveEndpoint("local"); err != nil {
		t.Fatalf("remove endpoint: %v", err)
	}
	if len(fc.Endpoints) != 1 {
		t.Errorf("endpoints = %d", len(fc.Endpoints))
	}
	if err := fc.RemoveEndpoint("local"); err == nil {
		t.Error("removing an endpoint twice was accepted")
	}
}

func TestUpdateEndpointRejectsAnInvalidChange(t *testing.T) {
	fc := testConfig()
	err := fc.UpdateEndpoint("minimax", func(e *EndpointRecord) error {
		e.BaseURL = ""
		return nil
	})
	if err == nil {
		t.Fatal("an endpoint without a base URL was accepted")
	}
	endpoint, _ := fc.EndpointByID("minimax")
	if endpoint.BaseURL != "https://api.minimax.io/v1" {
		t.Errorf("the refused change was applied anyway: %q", endpoint.BaseURL)
	}
}

func TestUpdateModelRejectsAnInvalidChange(t *testing.T) {
	fc := testConfig()
	err := fc.UpdateModel("minimax", "MiniMax-M3.1-Flash", func(m *ModelRecord) error {
		m.Effort = "enormous"
		return nil
	})
	if err == nil {
		t.Fatal("an unknown reasoning level was accepted")
	}
	model, _ := fc.ModelFor("minimax", "MiniMax-M3.1-Flash")
	if model.Effort != "medium" {
		t.Errorf("the refused change was applied anyway: %q", model.Effort)
	}
}

func TestActiveSelectionIsScoped(t *testing.T) {
	fc := testConfig()
	if fc.ActiveEndpointID() != "minimax" || fc.ActiveModelID() != "MiniMax-M3.1-Flash" {
		t.Fatalf("the active selection is wrong: %+v", fc.Selection)
	}
	fc.SetActive("local", "llama3.2")
	model, ok := fc.ActiveModelRecord()
	if !ok || model.ID != "llama3.2" {
		t.Fatalf("the active model = %+v", model)
	}
	endpoint, _ := fc.ActiveEndpoint()
	if endpoint.ID != "local" {
		t.Errorf("the active endpoint = %q", endpoint.ID)
	}
}

func TestCloneIsIndependent(t *testing.T) {
	fc := testConfig()
	clone := fc.Clone()
	clone.Endpoints[0].Name = "Changed"
	clone.Endpoints[0].Models[0].Effort = "high"
	clone.SetActive("local", "llama3.2")
	if fc.Endpoints[0].Name != "MiniMax" {
		t.Error("the clone shares the endpoint records")
	}
	if fc.Endpoints[0].Models[0].Effort != "medium" {
		t.Error("the clone shares the model records")
	}
	if fc.ActiveEndpointID() != "minimax" {
		t.Error("the clone shares the selection")
	}
}

func TestCredentialValuePrefersTheEnvironment(t *testing.T) {
	t.Setenv(EnvAPIKey, "from-the-environment")
	endpoint := EndpointRecord{CredentialEnv: "HAL_TEST_KEY", Credential: "literal"}
	t.Setenv("HAL_TEST_KEY", "from-the-named-variable")
	if got := endpoint.CredentialValue(); got != "from-the-environment" {
		t.Errorf("credential = %q", got)
	}
	os.Unsetenv(EnvAPIKey)
	if got := endpoint.CredentialValue(); got != "from-the-named-variable" {
		t.Errorf("credential = %q", got)
	}
}

func TestCredentialValueFallsBackToTheLiteralSecret(t *testing.T) {
	endpoint := EndpointRecord{Credential: "literal"}
	if got := endpoint.CredentialValue(); got != "literal" {
		t.Errorf("credential = %q", got)
	}
	if endpoint.HasCredential() != true {
		t.Error("HasCredential is wrong")
	}
	empty := EndpointRecord{}
	if empty.HasCredential() {
		t.Error("an endpoint without a credential claims one")
	}
}

func TestLegacyCredentialResolvesAnEnvironmentReference(t *testing.T) {
	t.Setenv("HAL_TEST_KEY", "the-secret")
	fc := FileConfig{APIKey: "$HAL_TEST_KEY"}
	if got := fc.CredentialValue(); got != "the-secret" {
		t.Errorf("credential = %q", got)
	}
	fc = FileConfig{APIKey: "${HAL_TEST_KEY}"}
	if got := fc.CredentialValue(); got != "the-secret" {
		t.Errorf("credential = %q", got)
	}
	fc = FileConfig{APIKey: "literal-key"}
	if got := fc.CredentialValue(); got != "literal-key" {
		t.Errorf("credential = %q", got)
	}
}

func TestReviewNoteNamesTheVendorMismatch(t *testing.T) {
	note := ReviewNote("https://api.minimax.io/v1", "gpt-4o")
	if note == "" {
		t.Fatal("a model of another vendor was not flagged")
	}
	if !strings.Contains(note, "OpenAI") {
		t.Errorf("the note does not name the vendor: %q", note)
	}
	if ReviewNote("https://api.minimax.io/v1", "MiniMax-M3.1-Flash") != "" {
		t.Error("a matching model was flagged")
	}
	if ReviewNote("http://127.0.0.1:11434/v1", "deepseek-r1") != "" {
		t.Error("a model on a local endpoint was flagged")
	}
	if ReviewNote("https://openrouter.ai/api/v1", "deepseek-r1") != "" {
		t.Error("a model on an aggregator endpoint was flagged")
	}
}

func TestReviewNoteNamesATypo(t *testing.T) {
	note := ReviewNote("https://api.minimax.io/v1", "deekseek-flash")
	if note == "" {
		t.Fatal("a mistyped identifier was not flagged")
	}
	if !strings.Contains(note, "deepseek-flash") {
		t.Errorf("the note does not name the likely identifier: %q", note)
	}
}

func TestEffectiveWireProtocol(t *testing.T) {
	cases := []struct {
		endpoint EndpointRecord
		want     WireProtocol
	}{
		{EndpointRecord{Protocol: ProtocolOpenAI}, WireOpenAI},
		{EndpointRecord{Protocol: ProtocolAnthropic}, WireAnthropic},
		{EndpointRecord{Protocol: ProtocolLocal}, WireOpenAI},
		{EndpointRecord{Protocol: ProtocolLocal, WireProtocol: WireAnthropic}, WireAnthropic},
		{EndpointRecord{Protocol: ProtocolCustom, WireProtocol: WireOpenAI}, WireOpenAI},
	}
	for _, tc := range cases {
		got, err := tc.endpoint.EffectiveWireProtocol()
		if err != nil {
			t.Errorf("%+v: %v", tc.endpoint, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%+v: wire = %q, want %q", tc.endpoint, got, tc.want)
		}
	}
	if _, err := (EndpointRecord{Protocol: ProtocolCustom}).EffectiveWireProtocol(); err == nil {
		t.Error("a custom endpoint without a wire protocol was accepted")
	}
}

func TestSyncLegacyMirrorDescribesTheActiveEndpoint(t *testing.T) {
	fc := testConfig()
	fc.SyncLegacyMirror()
	if fc.Endpoint != "https://api.minimax.io/v1" {
		t.Errorf("endpoint = %q", fc.Endpoint)
	}
	if fc.APIKey != "$MINIMAX_API_KEY" {
		t.Errorf("api key mirror = %q", fc.APIKey)
	}
	if fc.ActiveModel != "MiniMax-M3.1-Flash" {
		t.Errorf("active model = %q", fc.ActiveModel)
	}
	if len(fc.Models) != 1 || fc.Models[0].ID != "MiniMax-M3.1-Flash" {
		t.Errorf("models = %+v", fc.Models)
	}
	// The mirror never carries the per-model settings of the new schema.
	if fc.Models[0].ContextMode != "" {
		t.Errorf("the mirror carries a new-schema field: %+v", fc.Models[0])
	}
}

func TestEndpointsNeedingReview(t *testing.T) {
	fc := testConfig()
	if err := fc.UpdateModel("minimax", "MiniMax-M3.1-Flash", func(m *ModelRecord) error {
		m.Review = "check this one"
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	items := fc.EndpointsNeedingReview()
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Model != "MiniMax-M3.1-Flash" || items[0].Endpoint != "minimax" {
		t.Errorf("item = %+v", items[0])
	}
}
