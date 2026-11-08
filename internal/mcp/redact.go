package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A configured credential must never reach the model, the session file,
// the audit trail or the terminal, whatever channel a server uses to
// echo it. Redaction therefore runs on success text, error text,
// structured content, resource and prompt documents, image and audio
// payloads, annotations and unknown content types.
//
// A secret can appear in more than one representation: the literal
// value, the JSON-escaped form the protocol carries, a base64 payload or
// a URL-escaped form. Every representation is replaced. Work is bounded;
// if the bound is ever hit the unprocessed value is withheld rather than
// returned unscanned.

const (
	// minSecretLen is the shortest configured value that is treated as a
	// credential. Shorter values would rewrite ordinary text and destroy
	// more legitimate data than they protect.
	minSecretLen = 4
	// maxRedactNodes bounds the values one result may make the redactor
	// visit.
	maxRedactNodes = 50000
	// maxRedactBytes bounds the bytes one result may make the redactor
	// scan.
	maxRedactBytes = 16 << 20
)

// withheldText replaces a value the redactor refused to scan because a
// safety bound was reached.
const withheldText = "[content withheld: safety limit]"

// withheldContent is a complete content item used when a server item
// could not be scanned.
const withheldContent = `{"type":"text","text":"[content withheld: safety limit]"}`

// redactionVariants expands configured secrets into every textual form
// that can carry them. The result is deduplicated and sorted longest
// first, so a shorter secret can never rewrite part of a longer one.
func redactionVariants(secrets []string) []string {
	seen := make(map[string]bool, len(secrets)*4)
	out := make([]string, 0, len(secrets)*4)
	add := func(value string) {
		if len(value) < minSecretLen || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		add(secret)
		if escaped, err := json.Marshal(secret); err == nil && len(escaped) >= 2 {
			add(string(escaped[1 : len(escaped)-1]))
		}
		add(base64.StdEncoding.EncodeToString([]byte(secret)))
		add(base64.RawStdEncoding.EncodeToString([]byte(secret)))
		add(url.QueryEscape(secret))
		add(url.PathEscape(secret))
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// redact removes every secret from a short message.
func redact(text string, secrets []string) string {
	return replaceRedactions(text, redactionVariants(secrets))
}

// replaceRedactions substitutes every variant in one pass per variant.
func replaceRedactions(text string, variants []string) string {
	for _, variant := range variants {
		text = strings.ReplaceAll(text, variant, "***")
	}
	return text
}

// redactor scans structured values under a bounded budget.
type redactor struct {
	variants  []string
	nodes     int
	bytes     int
	exhausted bool
}

func newRedactor(secrets []string) *redactor {
	return &redactor{
		variants: redactionVariants(secrets),
		nodes:    maxRedactNodes,
		bytes:    maxRedactBytes,
	}
}

// text scans one string. A string larger than the remaining budget is
// withheld in full: truncating before scanning could return half a
// credential.
func (rd *redactor) text(value string) string {
	if rd == nil {
		return withheldText
	}
	if rd.exhausted || rd.nodes <= 0 || rd.bytes < len(value) {
		rd.exhausted = true
		return withheldText
	}
	rd.nodes--
	rd.bytes -= len(value)
	return replaceRedactions(value, rd.variants)
}

// value scans one decoded JSON value and returns its safe form.
func (rd *redactor) value(value any) any {
	if rd == nil {
		return withheldText
	}
	if rd.exhausted || rd.nodes <= 0 {
		rd.exhausted = true
		return withheldText
	}
	switch v := value.(type) {
	case string:
		return rd.text(v)
	case map[string]any:
		rd.nodes--
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(v))
		for _, key := range keys {
			child := v[key]
			if rd.exhausted {
				out[withheldText] = withheldText
				break
			}
			safeKey := rd.text(key)
			out[safeKey] = rd.value(child)
		}
		return out
	case []any:
		rd.nodes--
		out := make([]any, 0, len(v))
		for _, item := range v {
			if rd.exhausted {
				out = append(out, withheldText)
				break
			}
			out = append(out, rd.value(item))
		}
		return out
	default:
		rd.nodes--
		return value
	}
}

// jsonDocument decodes, redacts and re-encodes a JSON document. A
// document that cannot be scanned is replaced instead of returned raw.
func (rd *redactor) jsonDocument(raw string) (string, bool) {
	if rd == nil || rd.exhausted {
		return `"` + withheldText + `"`, false
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return `"` + withheldText + `"`, false
	}
	clean := rd.value(value)
	if rd.exhausted {
		return `"` + withheldText + `"`, false
	}
	out, err := json.Marshal(clean)
	if err != nil {
		return `"` + withheldText + `"`, false
	}
	return string(out), true
}

// contentPieces renders each server content item as redacted JSON.
//
// The item is decoded and redacted structurally, so annotations, meta,
// embedded resource text, image/audio payloads and content types HAL
// does not know are all covered.
func (rd *redactor) contentPieces(items []sdk.Content) ([]string, bool) {
	pieces := make([]string, 0, len(items))
	complete := true
	for _, item := range items {
		if item == nil {
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			complete = false
			pieces = append(pieces, withheldContent)
			continue
		}
		clean, ok := rd.jsonDocument(string(raw))
		if !ok {
			complete = false
			pieces = append(pieces, withheldContent)
			continue
		}
		pieces = append(pieces, clean)
	}
	if rd.exhausted {
		complete = false
	}
	return pieces, complete
}
