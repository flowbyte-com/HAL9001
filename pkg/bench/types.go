package bench

import (
	"time"
)

const SchemaVersion = "deepclaw-bench-003f.1"

// TrialSpec is the complete, serializable input to one isolated trial process.
type TrialSpec struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	TrialID       string `json:"trial_id"`
	Experiment    string `json:"experiment"`
	Condition     string `json:"condition"`
	Scenario      string `json:"scenario"`
	ProviderKind  string `json:"provider_kind"` // mock|live

	ProviderConfigPath string `json:"provider_config_path,omitempty"`
	ModelName          string `json:"model_name"`
	MPMBinary          string `json:"mpm_binary,omitempty"`

	SessionKey     string `json:"session_key"`
	TrialDir       string `json:"trial_dir"`
	AgentWorkspace string `json:"agent_workspace"`
	PicoclawHome   string `json:"picoclaw_home"`
	MPMWorkspace   string `json:"mpm_workspace,omitempty"`
	MPMHome        string `json:"mpm_home,omitempty"`
	LogPath        string `json:"log_path"`
	RawPath        string `json:"raw_path"`
	ResultPath     string `json:"result_path"`

	MemoryPolicy string `json:"memory_policy"` // none|eager|mpm|mpm_active
	ToolProfile  string `json:"tool_profile"`  // standard|coding
	Isolation    bool   `json:"isolation"`

	MPMProject     string `json:"mpm_project,omitempty"`
	ForeignProject string `json:"foreign_project,omitempty"`

	// Fixture content, also used by preflight checks and mock responses.
	ExpectedAnswer  string   `json:"expected_answer,omitempty"`
	ForbiddenAnswer string   `json:"forbidden_answer,omitempty"`
	Abstain         bool     `json:"abstain"`
	FixtureProbe    string   `json:"fixture_probe,omitempty"`
	MPMProbe        string   `json:"mpm_probe,omitempty"`
	Facts           []string `json:"facts,omitempty"`
	ForeignFacts    []string `json:"foreign_facts,omitempty"`

	Turns []TurnSpec `json:"turns"`

	TaskState       bool `json:"task_state,omitempty"`
	TaskStateBudget int  `json:"task_state_budget,omitempty"`

	MaxTokens         int `json:"max_tokens"`
	MaxToolIterations int `json:"max_tool_iterations"`
	ContextWindow     int `json:"context_window"`

	CodingExpectedFile    string `json:"coding_expected_file,omitempty"`
	CodingExpectedContent string `json:"coding_expected_content,omitempty"`
}

type TurnSpec struct {
	ID                 string `json:"id"`
	Prompt             string `json:"prompt"`
	ClearHistoryBefore bool   `json:"clear_history_before,omitempty"`
	RestartBefore      bool   `json:"restart_before,omitempty"`
	// CompactBefore runs an explicit /compact command through the production
	// command path before the turn is sent.
	CompactBefore bool `json:"compact_before,omitempty"`
}

type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type Provenance struct {
	GitCommit   string `json:"git_commit"`
	GitDirty    bool   `json:"git_dirty"`
	GitStatus   string `json:"git_status,omitempty"`
	GoVersion   string `json:"go_version"`
	BinaryPath  string `json:"binary_path"`
	BinarySHA   string `json:"binary_sha256"`
	BenchBinary string `json:"bench_binary"`
	BuiltAt     string `json:"built_at,omitempty"`
}

type Settings struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	MemoryPolicy    string   `json:"memory_policy"`
	ToolProfile     string   `json:"tool_profile"`
	Isolation       bool     `json:"isolation"`
	MPMProject      string   `json:"mpm_project,omitempty"`
	SessionKey      string   `json:"session_key"`
	MaxTokens       int      `json:"max_tokens"`
	MaxIterations   int      `json:"max_tool_iterations"`
	ContextWindow   int      `json:"context_window"`
	ActiveMemory    bool     `json:"active_memory"`
	TaskState       bool     `json:"task_state"`
	TaskStateBudget int      `json:"task_state_budget,omitempty"`
	ToolNames       []string `json:"tool_names"`
	ToolSchemaBytes int      `json:"tool_schema_bytes"`
	ToolSchemaSHA   string   `json:"tool_schema_sha256"`
}

type UsageRecord struct {
	// Reported is true only when the provider adapter returned a usage object.
	Reported bool `json:"reported"`
	// Fields are pointers so a missing usage object is null rather than zero.
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	TotalTokens         *int `json:"total_tokens"`
	CacheReadTokens     *int `json:"cache_read_tokens"`
	CacheCreationTokens *int `json:"cache_creation_tokens"`
	// CacheZeroMayBeMissing notes that the adapter exposes cache fields as
	// plain integers, so a reported zero cannot be distinguished from an
	// omitted cache field.
	CacheZeroMayBeMissing bool `json:"cache_zero_may_be_missing"`
}

type RetrievedBlock struct {
	Kind            string   `json:"kind"` // mpm|active
	RecordIDs       []string `json:"record_ids"`
	RecordCount     int      `json:"record_count"`
	EstimatedTokens int      `json:"estimated_tokens"`
	Chars           int      `json:"chars"`
}

type RequestRecord struct {
	RequestID string `json:"request_id"`
	TurnID    string `json:"turn_id"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	StartedAt string `json:"started_at"`
	LatencyMS int64  `json:"latency_ms"`

	Messages            int            `json:"messages"`
	Roles               map[string]int `json:"roles"`
	SystemChars         int            `json:"system_prompt_chars"`
	HistoryChars        int            `json:"history_chars"`
	MessageChars        int            `json:"message_chars"`
	ToolCount           int            `json:"tool_count"`
	ToolSchemaBytes     int            `json:"tool_schema_bytes"`
	ToolTokenEstimate   int            `json:"tool_token_estimate"`
	PromptTokenEstimate int            `json:"prompt_token_estimate"`

	Retrieved               []RetrievedBlock `json:"retrieved"`
	RetrievedIDs            []string         `json:"retrieved_ids"`
	RetrievalCount          int              `json:"retrieval_count"`
	RetrievalTokensEstimate int              `json:"retrieval_tokens_estimate"`
	RetrievalLatencyMS      int              `json:"retrieval_latency_ms"`
	ActiveMemoryChars       int              `json:"active_memory_chars"`
	ActiveMemoryTokens      int              `json:"active_memory_tokens_estimate"`

	Usage           UsageRecord      `json:"usage"`
	CompletionChars int              `json:"completion_chars"`
	FinishReason    string           `json:"finish_reason"`
	RequestError    string           `json:"request_error,omitempty"`
	ToolCalls       []ToolCallRecord `json:"tool_calls"`
}

type ToolCallRecord struct {
	TurnID     string         `json:"turn_id,omitempty"`
	Tool       string         `json:"tool"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	IsError    bool           `json:"is_error"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	OutcomeLen int            `json:"outcome_len,omitempty"`
	Source     string         `json:"source"` // provider|event
}

type TurnResult struct {
	TurnID     string `json:"turn_id"`
	Prompt     string `json:"prompt"`
	Response   string `json:"response,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`

	HistoryBefore      int  `json:"history_before"`
	HistoryAfter       int  `json:"history_after"`
	SummaryCharsBefore int  `json:"summary_chars_before"`
	SummaryCharsAfter  int  `json:"summary_chars_after"`
	HistoryCleared     bool `json:"history_cleared,omitempty"`
	Restarted          bool `json:"restarted,omitempty"`
	Compacted          bool `json:"compacted,omitempty"`
	Requests           int  `json:"llm_requests"`

	// TaskState is the sidecar snapshot after the turn, when task state is
	// enabled. It is evidence for the mechanism, not a model-quality score.
	TaskState *TaskStateSnapshot `json:"task_state,omitempty"`
	ToolCalls []ToolCallRecord   `json:"tool_calls,omitempty"`
}

type Score struct {
	Correct       bool     `json:"correct"`
	Abstained     bool     `json:"abstained"`
	AbstainOK     bool     `json:"abstain_ok"`
	ExpectedMatch bool     `json:"expected_match"`
	ForbiddenHit  bool     `json:"forbidden_hit"`
	CodeLike      string   `json:"code_like,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	CodingMatch   bool     `json:"coding_match,omitempty"`
}

// TaskStateSnapshot is a deterministic view of the session sidecar after a
// turn. RenderedText is what a prompt with no visible history could carry.
type TaskStateSnapshot struct {
	Available           bool     `json:"available"`
	Entries             int      `json:"entries"`
	ActiveEntries       int      `json:"active_entries"`
	Objective           string   `json:"objective,omitempty"`
	ObjectiveStatus     string   `json:"objective_status,omitempty"`
	ActiveKinds         []string `json:"active_kinds,omitempty"`
	ActiveVerifications []string `json:"active_verifications,omitempty"`
	ActiveKeys          []string `json:"active_keys,omitempty"`
	RenderedChars       int      `json:"rendered_chars"`
	RenderedTokens      int      `json:"rendered_tokens_estimate"`
	RenderedText        string   `json:"rendered_text,omitempty"`
}

// TurnScore records the deterministic per-turn expectation for Experiment C.
type TurnScore struct {
	TurnID   string  `json:"turn_id"`
	Passed   bool    `json:"passed"`
	Checks   []Check `json:"checks"`
	Response string  `json:"response,omitempty"`
}

type TrialResult struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	TrialID       string `json:"trial_id"`
	Experiment    string `json:"experiment"`
	Condition     string `json:"condition"`
	Scenario      string `json:"scenario"`
	Status        string `json:"status"` // pass|fail|error
	Error         string `json:"error,omitempty"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at"`
	DurationMS    int64  `json:"duration_ms"`

	Provenance Provenance       `json:"provenance"`
	Settings   Settings         `json:"settings"`
	Preflight  []Check          `json:"preflight"`
	Postflight []Check          `json:"postflight"`
	Turns      []TurnResult     `json:"turns"`
	Requests   []RequestRecord  `json:"requests"`
	ToolCalls  []ToolCallRecord `json:"tool_calls"`
	Score      Score            `json:"score"`
	TurnScores []TurnScore      `json:"turn_scores,omitempty"`

	TaskStateFinal *TaskStateSnapshot `json:"task_state_final,omitempty"`

	SessionKeysObserved    []string `json:"session_keys_observed"`
	SessionFilesObserved   []string `json:"session_files_observed"`
	AnswerFoundInWorkspace bool     `json:"answer_found_in_workspace"`
	RawPath                string   `json:"raw_path"`
	LogPath                string   `json:"log_path"`
	ResultPath             string   `json:"result_path"`

	Totals TrialTotals `json:"totals"`
}

type TrialTotals struct {
	LLMRequests           int   `json:"llm_requests"`
	ToolCalls             int   `json:"tool_calls"`
	PromptTokens          int   `json:"prompt_tokens,omitempty"`
	CompletionTokens      int   `json:"completion_tokens,omitempty"`
	TotalTokens           int   `json:"total_tokens,omitempty"`
	CacheReadTokens       int   `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens   int   `json:"cache_creation_tokens,omitempty"`
	UsageReportedRequests int   `json:"usage_reported_requests"`
	UsageMissingRequests  int   `json:"usage_missing_requests"`
	RetrievalLatencyMS    int   `json:"retrieval_latency_ms"`
	E2EMS                 int64 `json:"e2e_ms"`

	// 004A task-state overhead. Zero when task state is disabled.
	TaskStateFinalEntries       int `json:"task_state_final_entries,omitempty"`
	TaskStateFinalActiveEntries int `json:"task_state_final_active_entries,omitempty"`
	TaskStateRenderedChars      int `json:"task_state_rendered_chars,omitempty"`
	TaskStateRenderedTokens     int `json:"task_state_rendered_tokens_estimate,omitempty"`
}

type RawRecord struct {
	RequestID string          `json:"request_id"`
	TurnID    string          `json:"turn_id"`
	StartedAt string          `json:"started_at"`
	LatencyMS int64           `json:"latency_ms"`
	Model     string          `json:"model"`
	Messages  []MessageRecord `json:"messages"`
	Tools     any             `json:"tools"`
	Response  ResponseRecord  `json:"response"`
	Error     string          `json:"error,omitempty"`
}

type MessageRecord struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolCalls  any    `json:"tool_calls,omitempty"`
}

type ResponseRecord struct {
	Content      string       `json:"content"`
	FinishReason string       `json:"finish_reason"`
	ToolCalls    any          `json:"tool_calls,omitempty"`
	Usage        *UsageRecord `json:"usage,omitempty"`
}

type RunSummary struct {
	SchemaVersion string            `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Experiment    string            `json:"experiment"`
	StartedAt     string            `json:"started_at"`
	EndedAt       string            `json:"ended_at"`
	Provenance    Provenance        `json:"provenance"`
	Trials        []TrialResult     `json:"trials"`
	Aggregates    []Aggregate       `json:"aggregates"`
	Validation    ValidationSummary `json:"validation"`
}

type ValidationSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	Errors int `json:"errors"`
}

type Aggregate struct {
	Condition            string   `json:"condition"`
	Scenario             string   `json:"scenario"`
	Trials               int      `json:"trials"`
	Correct              int      `json:"correct"`
	AbstainOK            int      `json:"abstain_ok"`
	ForbiddenHits        int      `json:"forbidden_hits"`
	LLMRequests          int      `json:"llm_requests"`
	MeanPromptTokens     *float64 `json:"mean_prompt_tokens"`
	MeanCompletionTokens *float64 `json:"mean_completion_tokens"`
	MeanTotalTokens      *float64 `json:"mean_total_tokens"`
	MeanE2EMS            *float64 `json:"mean_e2e_ms"`
	MeanRetrievalMS      *float64 `json:"mean_retrieval_ms"`
	UsageMissingTrials   int      `json:"usage_missing_trials"`

	MeanTaskStateEntries       *float64 `json:"mean_task_state_entries"`
	MeanTaskStateActiveEntries *float64 `json:"mean_task_state_active_entries"`
	MeanTaskStateTokens        *float64 `json:"mean_task_state_rendered_tokens"`
	MaxTaskStateTokens         int      `json:"max_task_state_rendered_tokens"`
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
