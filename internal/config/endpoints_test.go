package config

import (
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

// The context window is a total: the room for the answer comes out of
// it. An output limit that fills the whole window leaves no room for
// the request, so the two numbers contradict each other and the record
// is refused with both of them named.
func TestAModelRefusesAnOutputLimitThatFillsItsWindow(t *testing.T) {
	fc := testConfig()
	err := fc.UpdateModel("minimax", "MiniMax-M3.1-Flash", func(m *ModelRecord) error {
		m.ContextWindow = 4096
		m.MaxOutputTokens = 4096
		return nil
	})
	if err == nil {
		t.Fatal("a model whose answer fills its whole window was accepted")
	}
	if !strings.Contains(err.Error(), "does not fit inside the window") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Errorf("the refusal does not name the numbers: %v", err)
	}
	model, _ := fc.ModelFor("minimax", "MiniMax-M3.1-Flash")
	if model.ContextWindow != 0 || model.MaxOutputTokens != 0 {
		t.Errorf("the refused change was applied anyway: %+v", model)
	}

	// An output limit that leaves room is accepted.
	if err := fc.UpdateModel("minimax", "MiniMax-M3.1-Flash", func(m *ModelRecord) error {
		m.ContextWindow = 8192
		m.MaxOutputTokens = 4096
		return nil
	}); err != nil {
		t.Fatalf("a model whose answer fits inside its window was refused: %v", err)
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

// The endpoint's own credential wins. The global variable is a fallback
// for an endpoint that declares none, and never substitutes the
// credential of another endpoint.
func TestCredentialValuePrefersTheEndpointOwnCredential(t *testing.T) {
	t.Setenv(EnvAPIKey, "the-global-key")
	t.Setenv("HAL_TEST_KEY", "the-endpoint-key")
	endpoint := EndpointRecord{CredentialEnv: "HAL_TEST_KEY", Credential: "literal"}
	if got := endpoint.CredentialValue(); got != "the-endpoint-key" {
		t.Errorf("credential = %q, want the endpoint's own", got)
	}
	literal := EndpointRecord{Credential: "literal"}
	if got := literal.CredentialValue(); got != "literal" {
		t.Errorf("credential = %q, want the literal", got)
	}
}

func TestCredentialValueFallsBackToTheGlobalOnlyWithoutOne(t *testing.T) {
	t.Setenv(EnvAPIKey, "the-global-key")
	bare := EndpointRecord{}
	if got := bare.CredentialValue(); got != "the-global-key" {
		t.Errorf("credential = %q, want the global fallback", got)
	}
	// A named variable that is not set means this endpoint has no
	// credential: the global belongs to other endpoints.
	t.Setenv("HAL_TEST_KEY", "")
	named := EndpointRecord{CredentialEnv: "HAL_TEST_KEY"}
	if got := named.CredentialValue(); got != "" {
		t.Errorf("credential = %q, want none", got)
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

// A protocol that contradicts its wire protocol is refused, not
// silently resolved: HAL never pretends that an endpoint speaks a
// dialect it does not.
func TestAContradictingWireProtocolIsRefused(t *testing.T) {
	cases := []EndpointRecord{
		{ID: "a", Name: "A", Protocol: ProtocolAnthropic, WireProtocol: WireOpenAI, BaseURL: "https://x/v1"},
		{ID: "b", Name: "B", Protocol: ProtocolOpenAI, WireProtocol: WireAnthropic, BaseURL: "https://x/v1"},
		{ID: "c", Name: "C", Protocol: ProtocolAnthropic, WireProtocol: "grpc", BaseURL: "https://x/v1"},
	}
	for _, endpoint := range cases {
		if _, err := endpoint.EffectiveWireProtocol(); err == nil {
			t.Errorf("%+v: the contradiction was accepted", endpoint)
		}
		if err := endpoint.Validate(); err == nil {
			t.Errorf("%+v: the contradiction was accepted by the validation", endpoint)
		}
		fc := testConfig()
		if err := fc.AddEndpoint(endpoint); err == nil {
			t.Errorf("%+v: the contradiction was written to the configuration", endpoint)
		}
	}
	// The same protocol and wire protocol are accepted.
	ok := EndpointRecord{
		ID: "d", Name: "D", Protocol: ProtocolAnthropic,
		WireProtocol: WireAnthropic, BaseURL: "https://x/v1",
	}
	if _, err := ok.EffectiveWireProtocol(); err != nil {
		t.Errorf("the matching pair was refused: %v", err)
	}
}

// A version-2 configuration without an active endpoint clears the
// legacy mirror. Leaving the address of a removed endpoint in the file
// would bring that endpoint back at the next launch.
func TestTheLegacyMirrorIsClearedWithoutAnActiveEndpoint(t *testing.T) {
	fc := testConfig()
	fc.Endpoint = "https://api.minimax.io/v1"
	fc.APIKey = "$MINIMAX_API_KEY"
	fc.ActiveModel = "MiniMax-M3.1-Flash"
	fc.Models = []ModelRecord{{ID: "MiniMax-M3.1-Flash"}}
	fc.Selection = nil

	fc.SyncLegacyMirror()
	if fc.Endpoint != "" {
		t.Errorf("the mirror still holds the address %q", fc.Endpoint)
	}
	if fc.APIKey != "" {
		t.Errorf("the mirror still holds the credential %q", fc.APIKey)
	}
	if fc.Provider != "" {
		t.Errorf("the mirror still holds the provider %q", fc.Provider)
	}
	if fc.ActiveModel != "" {
		t.Errorf("the mirror still holds the active model %q", fc.ActiveModel)
	}
	if len(fc.Models) != 0 {
		t.Errorf("the mirror still holds models: %+v", fc.Models)
	}
	// The endpoints themselves are untouched.
	if len(fc.Endpoints) != 2 {
		t.Errorf("the endpoints changed: %+v", fc.Endpoints)
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
