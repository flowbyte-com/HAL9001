package memory

import (
	"strings"
	"testing"
)

func validRecord() Record {
	return Record{
		Tier:    TierDeveloping,
		Subject: "canary deploy thread",
		Body:    "Considering a canary step; open: detection window.",
		Session: "cli:default",
	}
}

func TestValidateTiers(t *testing.T) {
	for _, tier := range []Tier{TierSpark, TierDeveloping, TierDecision, TierReview} {
		r := validRecord()
		r.Tier = tier
		if err := r.Validate(); err != nil {
			t.Errorf("tier %q rejected: %v", tier, err)
		}
	}
	r := validRecord()
	r.Tier = "hunch"
	if err := r.Validate(); err == nil {
		t.Error("unknown tier must be rejected")
	}
}

func TestValidateProvenance(t *testing.T) {
	for name, mutate := range map[string]func(*Record){
		"empty subject": func(r *Record) { r.Subject = " " },
		"empty body":    func(r *Record) { r.Body = "" },
		"no session":    func(r *Record) { r.Session = "" },
	} {
		r := validRecord()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestSensitiveTierExcluded(t *testing.T) {
	r := validRecord()
	r.Body = "The deploy uses api_key ABC123 for the registry."
	if err := r.Validate(); err == nil {
		t.Fatal("credentials must never be durable candidates")
	} else if !strings.Contains(err.Error(), "sensitive") {
		t.Errorf("rejection should name the rule, got: %v", err)
	}
	r = validRecord()
	r.Subject = "database password rotation"
	if err := r.Validate(); err == nil {
		t.Error("sensitive subjects must be rejected too")
	}
}
