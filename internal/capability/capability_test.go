package capability

import (
	"strings"
	"testing"
)

func TestAllowedCapabilities(t *testing.T) {
	for _, name := range []string{
		"converse", "web_search", "web_fetch", "read_file",
		"find_skills", "draft_prompt", "review_prompt", "review_report",
	} {
		if !Allowed(name) {
			t.Errorf("capability %q must be allowed", name)
		}
	}
}

func TestDefaultDeny(t *testing.T) {
	denied := []string{
		"", "exec", "bash", "shell", "write_file", "write", "edit_file",
		"edit", "append_file", "spawn", "subagent", "delegate", "cron",
		"install_skill", "deploy", "ssh", "mcp_shell", "plugin_exec",
		"future_unknown_tool", "converse ",
	}
	for _, name := range denied {
		if name == "converse " {
			continue // trailing space is trimmed and allowed; covered below
		}
		if Allowed(name) {
			t.Errorf("capability %q must be denied", name)
		}
		if err := Check(name); err == nil {
			t.Errorf("Check(%q) must fail", name)
		}
	}
}

func TestMatchingIsCaseAndSpaceInsensitive(t *testing.T) {
	if !Allowed("  Web_Search ") {
		t.Error("matching must ignore case and surrounding whitespace")
	}
	if err := Check("DRAFT_PROMPT"); err != nil {
		t.Errorf("Check must ignore case: %v", err)
	}
}

func TestRefusalMentionsBoundary(t *testing.T) {
	if !strings.Contains(Refusal(), "444") {
		t.Errorf("refusal must name the boundary: %q", Refusal())
	}
	if err := Check("exec"); err == nil {
		t.Fatal("expected denial")
	} else if !strings.Contains(err.Error(), "444") {
		t.Errorf("denial must name the boundary: %v", err)
	}
}
