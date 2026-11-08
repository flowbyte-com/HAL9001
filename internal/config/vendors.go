package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// vendorPrefix is a model-identifier prefix and the vendor that owns it.
//
// The list exists for one purpose: to notice a model identifier that
// belongs to a different provider than the endpoint it was configured
// under. It is a review aid, never a decision: HAL never moves a model
// because of it, it only tells the operator to look.
type vendorPrefix struct {
	prefix string
	vendor string
}

// vendorPrefixes lists the model-identifier prefixes HAL recognises.
var vendorPrefixes = []vendorPrefix{
	{"minimax", "minimax"},
	{"deepseek", "deepseek"},
	{"claude", "anthropic"},
	{"gpt", "openai"},
	{"o1", "openai"},
	{"o3", "openai"},
	{"gemini", "google"},
	{"llama", "meta"},
	{"mistral", "mistral"},
	{"mixtral", "mistral"},
	{"qwen", "alibaba"},
	{"qwq", "alibaba"},
	{"kimi", "moonshot"},
	{"moonshot", "moonshot"},
	{"glm", "zhipu"},
	{"grok", "xai"},
	{"doubao", "bytedance"},
	{"ernie", "baidu"},
	{"command", "cohere"},
	{"phi", "microsoft"},
	{"yi-", "01ai"},
	{"abab", "baichuan"},
	{"hunyuan", "tencent"},
	{"internlm", "shanghai-ai"},
	{"step", "stepfun"},
}

// aggregatorVendors are vendors whose endpoints legitimately serve other
// vendors' models. A model identifier that names a different vendor is
// not a warning there.
var aggregatorVendors = map[string]bool{
	"openrouter": true,
	"together":   true,
	"litellm":    true,
	"hyperbolic": true,
	"novita":     true,
	"vercel":     true,
	"fireworks":  true,
	"deepinfra":  true,
	"requesty":   true,
	"replicate":  true,
	"localhost":  true,
}

// endpointVendor guesses the vendor behind a base URL from its host.
//
// It is a guess, and it is only used to raise a review note: a host HAL
// does not recognise yields an empty vendor and no warning at all.
func endpointVendor(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	if host == "localhost" || net.ParseIP(host) != nil {
		return "local"
	}
	labels := strings.Split(host, ".")
	if len(labels) >= 2 {
		return labels[len(labels)-2]
	}
	return labels[0]
}

// modelVendor returns the vendor that owns a model identifier, and whether
// the identifier matched a known prefix at all.
func modelVendor(modelID string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return "", false
	}
	for _, vp := range vendorPrefixes {
		if strings.HasPrefix(id, vp.prefix) {
			return vp.vendor, true
		}
	}
	return "", false
}

// nearVendor returns the vendor whose identifier prefix the model id is
// within one edit of, together with the corrected spelling.
//
// It catches the common mistake of a mistyped identifier, such as
// "deekseek-flash" for "deepseek-flash".
func nearVendor(modelID string) (vendor string, corrected string, ok bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return "", "", false
	}
	best := 0
	for _, vp := range vendorPrefixes {
		if len(vp.prefix) < 5 || len(id) < len(vp.prefix) {
			continue
		}
		distance := editDistance(id[:len(vp.prefix)], vp.prefix)
		if distance != 1 {
			continue
		}
		if !ok || len(vp.prefix) > best {
			best = len(vp.prefix)
			vendor = vp.vendor
			corrected = vp.prefix + id[len(vp.prefix):]
			ok = true
		}
	}
	return vendor, corrected, ok
}

// editDistance is the Levenshtein distance between two strings.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

// min3 returns the smallest of three numbers.
func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// ReviewNote returns the operator-review note for a model identifier on an
// endpoint, or an empty string when nothing about it looks questionable.
//
// Two things raise a note. A model whose identifier names one vendor while
// the endpoint belongs to another is very likely configured in the wrong
// place. And an identifier that is one edit away from a known vendor's
// naming is very likely a typo. Neither is acted upon: HAL only says so,
// and the operator decides.
func ReviewNote(baseURL, modelID string) string {
	id := strings.TrimSpace(modelID)
	if id == "" {
		return ""
	}
	epVendor := endpointVendor(baseURL)
	if epVendor == "" || epVendor == "local" || aggregatorVendors[epVendor] {
		return ""
	}
	if mv, ok := modelVendor(id); ok {
		if mv == epVendor {
			return ""
		}
		return fmt.Sprintf("this identifier names a %s model, but the endpoint is %s — verify it or move it to its own endpoint",
			vendorLabel(mv), vendorLabel(epVendor))
	}
	if mv, corrected, ok := nearVendor(id); ok {
		if mv == epVendor {
			return ""
		}
		return fmt.Sprintf("possible typo: %q is a %s model, but the endpoint is %s — verify the identifier or move it to its own endpoint",
			corrected, vendorLabel(mv), vendorLabel(epVendor))
	}
	return ""
}

// vendorNames are the operator-facing spellings of the vendors HAL
// recognises.
var vendorNames = map[string]string{
	"minimax":     "MiniMax",
	"deepseek":    "DeepSeek",
	"openai":      "OpenAI",
	"anthropic":   "Anthropic",
	"google":      "Google",
	"moonshot":    "Moonshot",
	"meta":        "Meta",
	"alibaba":     "Alibaba",
	"mistral":     "Mistral",
	"zhipu":       "Zhipu",
	"xai":         "xAI",
	"bytedance":   "ByteDance",
	"baidu":       "Baidu",
	"cohere":      "Cohere",
	"microsoft":   "Microsoft",
	"01ai":        "01.AI",
	"baichuan":    "Baichuan",
	"tencent":     "Tencent",
	"shanghai-ai": "Shanghai AI",
	"stepfun":     "StepFun",
	"local":       "local",
}

// vendorLabel turns a vendor name into something an operator reads.
func vendorLabel(vendor string) string {
	if vendor == "" {
		return "unknown"
	}
	if name, ok := vendorNames[vendor]; ok {
		return name
	}
	return strings.ToUpper(vendor[:1]) + vendor[1:]
}
