package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// The provider origin of a conversation's reasoning.
//
// HAL speaks to many endpoints, and several of them serve the same
// model identifier through the same dialect: a Claude alias on
// DeepSeek's Anthropic-compatible endpoint is a Claude model on the
// Messages API, and a gateway in front of another gateway looks the
// same from the model name alone. The reasoning a provider sends is
// bound to the provider that sent it, so the origin of that reasoning
// has to be part of the record that carries it.
//
// The origin is a fingerprint of the endpoint itself: the wire it
// speaks, the address it answers on and the credential profile it
// uses. The same endpoint always yields the same origin, and two
// endpoints never share one.

// Origin identifies the provider origin of a conversation's reasoning.
//
// It is a fingerprint and nothing more: the address it was made from
// cannot be read back out of it, and it holds no secret of any kind.
type Origin string

// OriginFor returns the reasoning origin of a configured endpoint.
//
// The credential profile names the credential without naming it: it is
// the name of the environment variable that holds the credential, or a
// marker for a credential the configuration carries itself. The
// credential's value is never part of the origin — two accounts at one
// endpoint that the configuration cannot tell apart share an origin,
// and that limitation is documented rather than papered over.
func OriginFor(wire, baseURL, credentialProfile string) Origin {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(wire),
		endpointAddress(baseURL),
		strings.TrimSpace(credentialProfile),
	}, "\x00")))
	return Origin(hex.EncodeToString(sum[:12]))
}

// endpointAddress reduces an endpoint address to what identifies it:
// the scheme, the host and the path.
//
// The user information, the query and the fragment are left out. A
// secret in any of them is not part of the identity of the endpoint,
// and the identity must never carry one.
func endpointAddress(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// An address HAL cannot read is used as it is: it is still
		// an address, and two of them are two endpoints.
		return strings.TrimRight(raw, "/")
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	path := strings.TrimRight(u.Path, "/")
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + host + path
}

// endpointDomain is the registrable domain of an address: the last two
// labels of its host.
//
// It is what names the provider behind the address, and it is only
// ever a name: a host HAL does not recognise has no domain here, and
// nothing is claimed about it.
func endpointDomain(baseURL string) string {
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
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return ""
	}
	return labels[len(labels)-2] + "." + labels[len(labels)-1]
}

// endpointLabel names an endpoint for a diagnostic: its host and its
// path, without a scheme and without anything a secret could hide in.
func endpointLabel(baseURL string) string {
	address := endpointAddress(baseURL)
	address = strings.TrimPrefix(address, "https://")
	address = strings.TrimPrefix(address, "http://")
	if address == "" {
		return "the provider"
	}
	return address
}

// ThinkingDefault says how an endpoint thinks when a request says
// nothing about it.
//
// The three states are apart on purpose. An endpoint HAL knows thinks
// by default, an endpoint HAL knows thinks only when asked, and an
// endpoint HAL knows nothing about. The third is not the second: what
// an unknown endpoint does is that provider's business, and HAL does
// not guess it.
type ThinkingDefault int

const (
	// ThinkingUnknown: the endpoint's behaviour is not known.
	ThinkingUnknown ThinkingDefault = iota
	// ThinkingDefaultOn: the endpoint thinks when the request does not
	// ask for it.
	ThinkingDefaultOn
	// ThinkingDefaultOff: the endpoint thinks only when the request
	// asks for it.
	ThinkingDefaultOff
)

// String names the state for a diagnostic.
func (t ThinkingDefault) String() string {
	switch t {
	case ThinkingDefaultOn:
		return "on"
	case ThinkingDefaultOff:
		return "off"
	default:
		return "unknown"
	}
}

// thinkingDefaults names the endpoints whose thinking default HAL knows.
//
// The rule is the provider's own documentation, and it is a fact about
// the provider, never about the model identifier: DeepSeek's
// Anthropic-compatible endpoint enables thinking when the request says
// nothing about it, and the Messages API of Anthropic thinks only when
// the request asks.
//
// A host HAL does not know is an endpoint HAL knows nothing about. It
// is never treated as one of these, and it is never treated as the
// other.
var thinkingDefaults = map[string]ThinkingDefault{
	"deepseek.com":  ThinkingDefaultOn,
	"anthropic.com": ThinkingDefaultOff,
}

// ThinkingDefaultFor reports how the endpoint at this address thinks
// when a request says nothing about it.
//
// The answer is about the endpoint, never about the model: a Claude
// alias on DeepSeek's endpoint is a DeepSeek endpoint, and a DeepSeek
// model on a gateway is whatever that gateway is.
func ThinkingDefaultFor(baseURL string) ThinkingDefault {
	domain := endpointDomain(baseURL)
	if domain == "" {
		return ThinkingUnknown
	}
	return thinkingDefaults[domain]
}
