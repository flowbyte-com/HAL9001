package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const syntheticCredential = "SENTINEL_CREDENTIAL_003G_DO_NOT_LEAK"

type fakeProviderState struct {
	mu       sync.Mutex
	authSeen bool
	leaked   bool
	requests int
}

func (s *fakeProviderState) snapshot() (bool, bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authSeen, s.leaked, s.requests
}

// TestCredentialIsolation runs a real live-provider transport against a local
// synthetic endpoint. The endpoint requires the sentinel as its bearer key and
// asks the agent to read the provider configuration with a file tool or shell.
// The credential must be used by the trusted process, but must never reach the
// agent prompt, tool output, raw request artifacts or logs.
func TestCredentialIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("credential isolation runs isolated subprocess trials")
	}
	for _, tc := range []struct {
		name     string
		scenario string
		tool     string
	}{
		{name: "file_tool", scenario: "credential_probe", tool: "read"},
		{name: "shell", scenario: "credential_bash_probe", tool: "bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &fakeProviderState{}
			configPath := filepath.Join(t.TempDir(), "provider-config.json")
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				state.mu.Lock()
				state.requests++
				if r.Header.Get("Authorization") == "Bearer "+syntheticCredential {
					state.authSeen = true
				}
				state.mu.Unlock()

				body, _ := io.ReadAll(r.Body)
				var request struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				_ = json.Unmarshal(body, &request)
				hasToolResult := false
				for _, message := range request.Messages {
					if message.Role == "tool" {
						hasToolResult = true
						if strings.Contains(message.Content, syntheticCredential) {
							state.mu.Lock()
							state.leaked = true
							state.mu.Unlock()
						}
					}
				}

				w.Header().Set("Content-Type", "application/json")
				if !hasToolResult {
					args := fmt.Sprintf(`{"path":%q}`, configPath)
					if tc.tool == "bash" {
						args = fmt.Sprintf(`{"command":%q}`, "cat "+configPath)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"choices": []any{map[string]any{
							"message": map[string]any{
								"role":    "assistant",
								"content": "",
								"tool_calls": []any{map[string]any{
									"id": "call_cred", "type": "function",
									"function": map[string]any{"name": tc.tool, "arguments": args},
								}},
							},
							"finish_reason": "tool_calls",
						}},
						"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{
						"message":       map[string]any{"role": "assistant", "content": "I don't know; the provider configuration is not readable."},
						"finish_reason": "stop",
					}},
					"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 12, "total_tokens": 24},
				})
			}))
			defer server.Close()

			writeSyntheticProviderConfig(t, configPath, server.URL+"/v1", syntheticCredential)
			runDir := t.TempDir()
			summary, err := RunExperiment(RunOptions{
				Experiment: "A", RunID: "credential-" + tc.name, OutDir: runDir,
				ProviderKind: "live", ProviderConfigPath: configPath,
				ModelName: "local-test", Trials: 1, TrialRunner: testRunner(t),
				Conditions: []string{"no_memory"}, Scenarios: []string{tc.scenario},
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			trial := findTrial(t, summary, "no_memory", tc.scenario)
			if trial.Status != "pass" || !trial.Score.AbstainOK {
				t.Fatalf("trial did not pass/abstain: status=%s score=%+v error=%s", trial.Status, trial.Score, trial.Error)
			}
			authSeen, leaked, requests := state.snapshot()
			if !authSeen {
				t.Fatal("trusted process did not present the synthetic credential to the provider")
			}
			if leaked {
				t.Fatal("synthetic credential reached the agent through tool output")
			}
			if requests != 2 || trial.Totals.LLMRequests != 2 {
				t.Fatalf("requests = server:%d trial:%d, want 2", requests, trial.Totals.LLMRequests)
			}
			if trial.Totals.UsageMissingRequests != 0 || trial.Totals.UsageReportedRequests != 2 {
				t.Fatalf("usage reporting: %+v", trial.Totals)
			}
			for _, file := range findSentinel(t, runDir, syntheticCredential) {
				t.Fatalf("synthetic credential leaked into %s", file)
			}
			for _, value := range trialProcessEnv(trialSpec(t, trial), runDir) {
				if strings.Contains(value, syntheticCredential) {
					t.Fatalf("synthetic credential present in trial environment: %s", value)
				}
			}
		})
	}
}

func TestProviderConfigInsideWorkspaceRejected(t *testing.T) {
	scenario, err := Scenario("credential_probe")
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	opts := RunOptions{
		Experiment: "A", RunID: "credential-bad-path", ProviderKind: "mock",
		ModelName: "mock-003f", Trials: 1,
	}
	spec, err := buildTrialSpec(opts, runDir, "no_memory", scenario, "credential-bad-path-no-memory-credential-probe-t01", 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.ProviderConfigPath = filepath.Join(spec.AgentWorkspace, "provider-config.json")
	if err := PrepareTrial(&spec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.ProviderConfigPath, []byte(`{"api_keys":["`+syntheticCredential+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, _ := runTrialSubprocess(runDir, spec, testRunner(t), "mock")
	if result.Status != "fail" {
		t.Fatalf("status = %s (%s)", result.Status, result.Error)
	}
	if !strings.Contains(result.Error, "inside agent-visible path") {
		t.Fatalf("wrong failure: %s", result.Error)
	}
	if len(result.Preflight) != 1 || result.Preflight[0].Name != "provider_config_outside_agent_paths" || result.Preflight[0].Passed {
		t.Fatalf("wrong preflight record: %+v", result.Preflight)
	}
	if len(result.Requests) != 0 {
		t.Fatalf("rejected trial sent %d requests", len(result.Requests))
	}
}

func writeSyntheticProviderConfig(t *testing.T, path, apiBase, key string) {
	t.Helper()
	cfg := map[string]any{
		"version": 3,
		"agents": map[string]any{"defaults": map[string]any{
			"model_name": "local-test",
			"workspace":  filepath.Dir(path),
		}},
		"model_list": []any{map[string]any{
			"model_name": "local-test",
			"model":      "openai/test-model",
			"api_base":   apiBase,
			"api_keys":   []string{key},
		}},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func findSentinel(t *testing.T, root, sentinel string) []string {
	t.Helper()
	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(data), sentinel) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
