// Package memory defines HAL9001's durable-knowledge contracts. Durable
// memory distinguishes speculation, developing ideas, decisions, retrieved
// information and verified facts — never conflating them. MPM remains the
// intended long-term backend; this package establishes the record types,
// the admission rules and the store interface. No live writes happen here:
// every admission crosses an explicit operator approval boundary.
package memory

import (
	"fmt"
	"strings"
	"time"
)

// Tier ranks how settled a record is. Promotion is conservative: doubt
// means asking or leaving the thought in the conversation, never storing
// on a guess.
type Tier string

const (
	// TierSpark is an interesting but undeveloped observation. Sparks
	// expire rather than promote by age.
	TierSpark Tier = "spark"
	// TierDeveloping is a thread under active development: what the idea
	// currently is, what remains open, what was set aside. Updates
	// supersede — the thread is replaced, never accumulated.
	TierDeveloping Tier = "developing"
	// TierDecision is an established outcome: what was decided, why, and
	// on what basis. Immutable once written; a retraction writes a
	// superseding record, never an edit.
	TierDecision Tier = "decision"
	// TierReview records a review outcome or outstanding question with its
	// evidence pointers.
	TierReview Tier = "review"
)

// Record is one durable memory entry with its provenance attached.
type Record struct {
	Tier      Tier      `json:"tier"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	Session   string    `json:"session"`
	CreatedAt time.Time `json:"created_at"`
	// Supersedes links to the record this one replaces, if any.
	Supersedes string `json:"supersedes,omitempty"`
	// ID is assigned by the store on admission.
	ID string `json:"id,omitempty"`
}

// sensitiveMarkers names categories that are never durable-memory
// candidates at any tier, however often repeated.
var sensitiveMarkers = []string{
	"password", "passwd", "secret", "api_key", "apikey", "token",
	"private_key", "privatekey", "credential", "bearer",
}

// Validate enforces the admission rules that do not need an operator:
// tier membership, non-empty content, and the sensitive tier. Operator
// approval itself happens above this package, at the admission boundary.
func (r Record) Validate() error {
	switch r.Tier {
	case TierSpark, TierDeveloping, TierDecision, TierReview:
	default:
		return fmt.Errorf("memory: unknown tier %q", r.Tier)
	}
	if strings.TrimSpace(r.Subject) == "" {
		return fmt.Errorf("memory: empty subject")
	}
	if strings.TrimSpace(r.Body) == "" {
		return fmt.Errorf("memory: empty body")
	}
	if strings.TrimSpace(r.Session) == "" {
		return fmt.Errorf("memory: missing provenance (session)")
	}
	lowered := strings.ToLower(r.Subject + "\n" + r.Body)
	for _, marker := range sensitiveMarkers {
		if strings.Contains(lowered, marker) {
			return fmt.Errorf("memory: sensitive material is never a durable candidate (%q)", marker)
		}
	}
	return nil
}

// Store is the narrowly scoped persistence contract. Implementations
// (eventually MPM) admit records only through Admit, which the operator
// approval boundary guards; reads never mutate.
type Store interface {
	Admit(record Record) (Record, error)
	Supersede(id string, next Record) (Record, error)
	// Recall returns records relevant to the query. Retrieval is
	// read-only by contract.
	Recall(query string, limit int) ([]Record, error)
}
