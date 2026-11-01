package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type turnIDKey struct{}

func WithTurnID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, turnIDKey{}, id)
}

func TurnIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(turnIDKey{}).(string); ok {
		return id
	}
	return ""
}

// RecordingProvider captures every provider request/response pair the agent
// actually makes. It is the source of request counts and token usage; log lines
// are never used to count LLM calls.
type RecordingProvider struct {
	inner providers.LLMProvider
	spec  TrialSpec

	mu       sync.Mutex
	requests []RequestRecord
	raw      []RawRecord
	tools    []ToolCallRecord
	next     int

	// 005B token-budget enforcement state. reportedTrialTotal sums
	// provider-reported total_tokens for requests that completed in this
	// process; meteringFailed latches when a successful live response
	// carries no usable total, because that spend cannot be metered.
	reportedTrialTotal int64
	meteringFailed     bool
	budgetStop         string
}

func NewRecordingProvider(inner providers.LLMProvider, spec TrialSpec) *RecordingProvider {
	return &RecordingProvider{inner: inner, spec: spec}
}

func (p *RecordingProvider) GetDefaultModel() string { return p.inner.GetDefaultModel() }

// nextRequestAllowance is the conservative upper bound for one more request:
// the full context window plus the per-request completion cap. The next
// request is refused when the metered total plus this allowance would exceed
// a ceiling, so a ceiling can never be breached by sending one more request.
func (p *RecordingProvider) nextRequestAllowance() int64 {
	window := p.spec.ContextWindow
	if window <= 0 {
		window = 32768
	}
	maxTokens := p.spec.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return int64(window) + int64(maxTokens)
}

func (p *RecordingProvider) budgetCaps() (perTrial, total, before int64) {
	return p.spec.TokenBudgetPerTrial, p.spec.TokenBudgetTotal,
		p.spec.PilotReportedBefore + p.spec.TrialReportedBefore
}

// requestAllowed reports whether another provider request may be sent. A nil
// error means the request may proceed; the provider is never contacted when
// an error is returned.
func (p *RecordingProvider) requestAllowed() error {
	perTrial, total, before := p.budgetCaps()
	if perTrial <= 0 && total <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meteringFailed {
		return fmt.Errorf("token metering failure: a successful live response reported no usable total; stopping instead of treating spend as zero")
	}
	if p.budgetStop != "" {
		return fmt.Errorf("%s", p.budgetStop)
	}
	allowance := p.nextRequestAllowance()
	metered := before + p.reportedTrialTotal
	if perTrial > 0 && metered+allowance > perTrial {
		p.budgetStop = fmt.Sprintf("token budget exhausted: trial reported %d + allowance %d exceeds per-trial cap %d; refusing to send another request",
			p.reportedTrialTotal, allowance, perTrial)
		return fmt.Errorf("%s", p.budgetStop)
	}
	if total > 0 && metered+allowance > total {
		p.budgetStop = fmt.Sprintf("token budget exhausted: pilot reported %d + trial reported %d + allowance %d exceeds total cap %d; refusing to send another request",
			before, p.reportedTrialTotal, allowance, total)
		return fmt.Errorf("%s", p.budgetStop)
	}
	return nil
}

// BudgetStop reports whether the budget guard blocked a request.
func (p *RecordingProvider) BudgetStop() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.budgetStop != "" || p.meteringFailed, p.budgetStop
}

func (p *RecordingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	if err := p.requestAllowed(); err != nil {
		return nil, err
	}
	start := time.Now()
	turnID := TurnIDFromContext(ctx)
	requestID, raw, request := p.begin(turnID, model, messages, tools)
	response, err := p.inner.Chat(ctx, messages, tools, model, options)
	p.finish(requestID, raw, request, start, response, err)
	return response, err
}

func (p *RecordingProvider) ChatStream(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(string),
) (*providers.LLMResponse, error) {
	streamer, ok := p.inner.(providers.StreamingProvider)
	if !ok {
		return p.Chat(ctx, messages, tools, model, options)
	}
	if err := p.requestAllowed(); err != nil {
		return nil, err
	}
	start := time.Now()
	turnID := TurnIDFromContext(ctx)
	requestID, raw, request := p.begin(turnID, model, messages, tools)
	response, err := streamer.ChatStream(ctx, messages, tools, model, options, onChunk)
	p.finish(requestID, raw, request, start, response, err)
	return response, err
}

func (p *RecordingProvider) ChatStreamEvents(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(providers.StreamChunk),
) (*providers.LLMResponse, error) {
	streamer, ok := p.inner.(providers.StreamingEventProvider)
	if !ok {
		return p.Chat(ctx, messages, tools, model, options)
	}
	if err := p.requestAllowed(); err != nil {
		return nil, err
	}
	start := time.Now()
	turnID := TurnIDFromContext(ctx)
	requestID, raw, request := p.begin(turnID, model, messages, tools)
	response, err := streamer.ChatStreamEvents(ctx, messages, tools, model, options, onChunk)
	p.finish(requestID, raw, request, start, response, err)
	return response, err
}

func (p *RecordingProvider) SupportsNativeSearch() bool {
	if capable, ok := p.inner.(providers.NativeSearchCapable); ok {
		return capable.SupportsNativeSearch()
	}
	return false
}

func (p *RecordingProvider) SupportsThinking() bool {
	if capable, ok := p.inner.(providers.ThinkingCapable); ok {
		return capable.SupportsThinking()
	}
	return false
}

func (p *RecordingProvider) Close() {
	if closer, ok := p.inner.(providers.StatefulProvider); ok {
		closer.Close()
	}
}

func (p *RecordingProvider) begin(
	turnID, model string,
	messages []providers.Message,
	tools []providers.ToolDefinition,
) (string, RawRecord, RequestRecord) {
	p.mu.Lock()
	p.next++
	seq := p.next
	p.mu.Unlock()

	requestID := fmt.Sprintf("%sreq-%03d", p.spec.RequestIDPrefix, seq)
	if turnID == "" {
		turnID = "unknown"
	}
	started := nowRFC3339()

	roles := make(map[string]int)
	messageChars := 0
	historyChars := 0
	systemChars := 0
	for i, msg := range messages {
		roles[msg.Role]++
		messageChars += len(msg.Content)
		if msg.Role == "system" && i == 0 {
			systemChars = len(msg.Content)
		} else {
			historyChars += len(msg.Content)
		}
	}
	schemaData, _ := json.Marshal(tools)
	retrieved, ids, retrCount, retrTokens := extractRetrieval(messages)

	activeChars, activeTokens := retrievalBlockSizes(retrieved, "active")
	taskText, taskChars, taskTokens, taskEntries := taskStateBlockSizes(messages)
	request := RequestRecord{
		RequestID:               requestID,
		TurnID:                  turnID,
		Provider:                p.spec.ProviderKind,
		Model:                   model,
		StartedAt:               started,
		Messages:                len(messages),
		Roles:                   roles,
		SystemChars:             systemChars,
		HistoryChars:            historyChars,
		MessageChars:            messageChars,
		ToolCount:               len(tools),
		ToolSchemaBytes:         len(schemaData),
		ToolTokenEstimate:       agent.EstimateToolDefsTokens(tools),
		PromptTokenEstimate:     (messageChars + len(schemaData) + 3) / 4,
		Retrieved:               retrieved,
		RetrievedIDs:            ids,
		RetrievalCount:          retrCount,
		RetrievalTokensEstimate: retrTokens,
		ActiveMemoryChars:       activeChars,
		ActiveMemoryTokens:      activeTokens,
		TaskStateChars:          taskChars,
		TaskStateTokens:         taskTokens,
		TaskStateRenderedCount:  taskEntries,
		TaskStateText:           taskText,
	}

	raw := RawRecord{
		RequestID: requestID,
		TurnID:    turnID,
		StartedAt: started,
		Model:     model,
		Messages:  rawMessages(messages),
		Tools:     tools,
	}
	return requestID, raw, request
}

func (p *RecordingProvider) finish(
	requestID string,
	raw RawRecord,
	request RequestRecord,
	start time.Time,
	response *providers.LLMResponse,
	err error,
) {
	latency := time.Since(start).Milliseconds()
	request.LatencyMS = latency
	raw.LatencyMS = latency

	if response != nil {
		request.CompletionChars = len(response.Content)
		request.FinishReason = response.FinishReason
		usage := usageRecord(response.Usage)
		request.Usage = usage
		raw.Response = ResponseRecord{
			Content:      response.Content,
			FinishReason: response.FinishReason,
			ToolCalls:    response.ToolCalls,
			Usage:        ptrUsage(usage),
		}
		for _, call := range response.ToolCalls {
			args := map[string]any{}
			if call.Function != nil {
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
			}
			request.ToolCalls = append(request.ToolCalls, ToolCallRecord{
				TurnID: request.TurnID, Tool: toolCallName(call), Arguments: args, Source: "provider",
			})
		}
	}
	if err != nil {
		request.RequestError = err.Error()
		raw.Error = err.Error()
	}

	p.mu.Lock()
	// 005B metering: account provider-reported totals while holding the same
	// lock that gates the next request, so the check-then-send sequence in
	// requestAllowed cannot race a concurrent completion. A successful
	// response without a usable total latches a metering failure when caps
	// are active: its spend cannot be measured, so it must stop the trial
	// rather than count as zero. Transport errors are excluded because the
	// failed turn already ends the trial.
	if perTrial, total, _ := p.budgetCaps(); (perTrial > 0 || total > 0) && err == nil && response != nil {
		if request.Usage.Reported && request.Usage.TotalTokens != nil {
			p.reportedTrialTotal += int64(*request.Usage.TotalTokens)
		} else {
			p.meteringFailed = true
		}
	}
	p.requests = append(p.requests, request)
	p.raw = append(p.raw, raw)
	p.tools = append(p.tools, request.ToolCalls...)
	p.mu.Unlock()
}

func (p *RecordingProvider) Requests() []RequestRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]RequestRecord(nil), p.requests...)
	for i := range out {
		out[i].ToolCalls = append([]ToolCallRecord(nil), out[i].ToolCalls...)
	}
	return out
}

func (p *RecordingProvider) RawRecords() []RawRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]RawRecord(nil), p.raw...)
}

func (p *RecordingProvider) AddToolEvent(call ToolCallRecord) {
	p.mu.Lock()
	p.tools = append(p.tools, call)
	p.mu.Unlock()
}

func (p *RecordingProvider) ToolCalls() []ToolCallRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ToolCallRecord(nil), p.tools...)
}

func (p *RecordingProvider) WriteRaw(path string) error {
	records := p.RawRecords()
	var b strings.Builder
	for _, record := range records {
		data, err := json.Marshal(redactValue(record))
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return writeFilePrivate(path, []byte(b.String()))
}

func usageRecord(usage *providers.UsageInfo) UsageRecord {
	if usage == nil {
		return UsageRecord{Reported: false}
	}
	return UsageRecord{
		Reported:              true,
		PromptTokens:          intPtr(usage.PromptTokens),
		CompletionTokens:      intPtr(usage.CompletionTokens),
		TotalTokens:           intPtr(usage.TotalTokens),
		CacheReadTokens:       intPtr(usage.CacheReadTokens),
		CacheCreationTokens:   intPtr(usage.CacheCreationTokens),
		CacheZeroMayBeMissing: true,
	}
}

func ptrUsage(usage UsageRecord) *UsageRecord {
	if !usage.Reported {
		return nil
	}
	return &usage
}

func intPtr(value int) *int { return &value }

func rawMessages(messages []providers.Message) []MessageRecord {
	out := make([]MessageRecord, 0, len(messages))
	for _, msg := range messages {
		out = append(out, MessageRecord{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  msg.ToolCalls,
		})
	}
	return out
}

var mpmRecordID = regexp.MustCompile(`(?m)^- \(([^,)]+)`)
var activeRecordID = regexp.MustCompile(`(?m)^- \(([^)]+)\)`)

func retrievalBlockSizes(blocks []RetrievedBlock, kind string) (int, int) {
	chars := 0
	tokens := 0
	for _, block := range blocks {
		if block.Kind == kind {
			chars += block.Chars
			tokens += block.EstimatedTokens
		}
	}
	return chars, tokens
}

// taskStateBlockSizes measures the task.state prompt part without relying on
// log lines. It returns characters, estimated tokens and the number of rendered
// entry lines.
func taskStateBlockSizes(messages []providers.Message) (string, int, int, int) {
	const marker = "source: task.state):"
	for _, msg := range messages {
		idx := strings.Index(msg.Content, marker)
		if idx < 0 {
			continue
		}
		block := msg.Content[idx+len(marker):]
		if end := strings.Index(block, "\n\nQuestion: "); end >= 0 {
			block = block[:end]
		}
		entries := 0
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "- ") {
				entries++
			}
		}
		return strings.TrimSpace(block), len(block), (len(block) + 3) / 4, entries
	}
	return "", 0, 0, 0
}

func extractRetrieval(messages []providers.Message) ([]RetrievedBlock, []string, int, int) {
	blocks := make([]RetrievedBlock, 0, 2)
	var ids []string
	totalTokens := 0
	seen := map[string]struct{}{}
	addIDs := func(kind string, matches []string) {
		for _, id := range matches {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if kind == "active" && id == "none" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, msg := range messages {
		if idx := strings.Index(msg.Content, "## Retrieved MPM memories"); idx >= 0 {
			block := msg.Content[idx:]
			matches := mpmRecordID.FindAllStringSubmatch(block, -1)
			blockIDs := make([]string, 0, len(matches))
			for _, match := range matches {
				blockIDs = append(blockIDs, match[1])
			}
			tokens := (len(block) + 3) / 4
			totalTokens += tokens
			blocks = append(blocks, RetrievedBlock{Kind: "mpm", RecordIDs: blockIDs, RecordCount: len(blockIDs), EstimatedTokens: tokens, Chars: len(block)})
			addIDs("mpm", blockIDs)
		}
		if idx := strings.Index(msg.Content, "Active working memory"); idx >= 0 {
			block := msg.Content[idx:]
			matches := activeRecordID.FindAllStringSubmatch(block, -1)
			blockIDs := make([]string, 0, len(matches))
			for _, match := range matches {
				blockIDs = append(blockIDs, match[1])
			}
			tokens := (len(block) + 3) / 4
			totalTokens += tokens
			blocks = append(blocks, RetrievedBlock{Kind: "active", RecordIDs: blockIDs, RecordCount: len(blockIDs), EstimatedTokens: tokens, Chars: len(block)})
			addIDs("active", blockIDs)
		}
	}
	return blocks, ids, len(blocks), totalTokens
}

func redactValue(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return value
	}
	return redactAny(generic)
}

func redactAny(value any) any {
	switch v := value.(type) {
	case string:
		return RedactSecrets(v)
	case []any:
		for i := range v {
			v[i] = redactAny(v[i])
		}
		return v
	case map[string]any:
		for key, item := range v {
			if isSecretKey(key) {
				v[key] = "[REDACTED]"
				continue
			}
			v[key] = redactAny(item)
		}
		return v
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return lower == "api_key" || lower == "api_keys" || lower == "authorization" ||
		lower == "token" || lower == "access_token" || strings.HasSuffix(lower, "_api_key")
}

func writeFilePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func toolCallName(call providers.ToolCall) string {
	if call.Name != "" {
		return call.Name
	}
	if call.Function != nil {
		return call.Function.Name
	}
	return ""
}
