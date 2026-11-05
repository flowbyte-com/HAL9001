package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/usage"
)

func transcriptModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := testModel()
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(Model)
}

func allEntryRows(m Model) []string {
	var rows []string
	for _, e := range m.entries {
		rows = append(rows, e.rows...)
	}
	return rows
}

func visibleRow(rows []string, fragment string) (int, string) {
	for i, row := range rows {
		if strings.Contains(stripANSI(row), fragment) {
			return i, row
		}
	}
	return -1, ""
}

func TestTranscriptSharesOneContentColumn(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}} {
		t.Run(strconv.Itoa(size[0]), func(t *testing.T) {
			m := transcriptModel(t, size[0], size[1])
			m.SetHistory([]SessionLine{
				{Role: "user", Text: "user words here", TurnID: "t1"},
				{Role: "assistant", Text: "assistant words here", TurnID: "t1"},
			})
			rows := m.layoutRows()
			_, user := visibleRow(rows, "user words here")
			_, assistant := visibleRow(rows, "assistant words here")
			_, eye := visibleRow(rows, "◉")
			if user == "" || assistant == "" || eye == "" {
				t.Fatalf("%dx%d: transcript rows missing:\n%s", size[0], size[1],
					stripANSI(strings.Join(rows, "\n")))
			}
			if got := displayColumn(user, "user words here"); got != m.contentColumn() {
				t.Errorf("user starts at %d, want %d", got, m.contentColumn())
			}
			if got := displayColumn(assistant, "assistant words here"); got != m.contentColumn() {
				t.Errorf("assistant starts at %d, want %d", got, m.contentColumn())
			}
			if got := displayColumn(eye, "◉"); got != m.contentColumn()-2 {
				t.Errorf("eye starts at %d, want %d", got, m.contentColumn()-2)
			}
			if strings.Contains(stripANSI(strings.Join(rows, "\n")), "You >") ||
				strings.Contains(stripANSI(strings.Join(rows, "\n")), "HAL >") {
				t.Error("speaker labels are still visible")
			}
			for _, row := range rows {
				if got := lipgloss.Width(row); got > size[0] {
					t.Errorf("%dx%d: row is %d cells: %q", size[0], size[1], got, stripANSI(row))
				}
			}
		})
	}
}

func TestTranscriptEyeMarksOnlyAssistantRows(t *testing.T) {
	m := transcriptModel(t, 80, 24)
	m.SetHistory([]SessionLine{
		{Role: "user", Text: "first question", TurnID: "t1"},
		{Role: "assistant", Text: "first answer", TurnID: "t1"},
		{Role: "user", Text: "second question", TurnID: "t2"},
		{Role: "assistant", Text: "second answer", TurnID: "t2"},
	})
	plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if got := strings.Count(plain, "◉"); got != 2 {
		t.Fatalf("eyes = %d, want one per assistant response:\n%s", got, plain)
	}
	if strings.Index(plain, "◉") > strings.Index(plain, "first answer") {
		t.Error("the first eye is not at the start of its assistant block")
	}
	if strings.Count(plain, "first answer") != 1 || strings.Count(plain, "second answer") != 1 {
		t.Error("assistant text was altered by the marker")
	}
}

func TestTranscriptWrappedRowsKeepTheColumn(t *testing.T) {
	m := transcriptModel(t, 60, 24)
	long := "wrapped " + strings.Repeat("word ", 18) + "finalanchor"
	m.SetHistory([]SessionLine{
		{Role: "user", Text: long, TurnID: "t1"},
		{Role: "assistant", Text: long, TurnID: "t1"},
	})
	rows := allEntryRows(m)
	_, userRow := visibleRow(rows, "finalanchor")
	if userRow == "" {
		t.Fatalf("the user's last row is not visible:\n%s", stripANSI(strings.Join(rows, "\n")))
	}
	// There are two copies of finalanchor (user and assistant); check
	// every row carrying it starts at the content column.
	for _, row := range rows {
		if strings.Contains(stripANSI(row), "finalanchor") {
			fields := strings.Fields(stripANSI(row))
			if len(fields) == 0 {
				continue
			}
			if got := displayColumn(row, fields[0]); got != m.contentColumn() {
				t.Errorf("wrapped row starts at %d, want %d: %q", got, m.contentColumn(), stripANSI(row))
			}
		}
	}
}

func TestTranscriptToolAndMetadataOrder(t *testing.T) {
	m := transcriptModel(t, 80, 24)
	m.SetUsage(usage.Session{History: []usage.Turn{{
		Input: 10, Output: 20, Duration: 1000, Reported: true,
	}}})
	m.SetHistory([]SessionLine{
		{Role: "user", Text: "run it", TurnID: "t1"},
		{
			Role: "assistant", Text: "the answer is ready", TurnID: "t1",
			Tools: []session.ToolExecution{{
				TurnID: "t1", Tool: "bash", Action: "bash", Mode: "666",
				Status: session.ToolSuccess, ExitCode: intPointer(0),
				Summary: "exit 0",
			}},
		},
	})
	rows := m.layoutRows()
	answerAt, answer := visibleRow(rows, "the answer is ready")
	toolAt, tool := visibleRow(rows, "⚙ bash")
	metaAt, meta := visibleRow(rows, "1s · 10 in / 20 out")
	if answerAt < 0 || toolAt < 0 || metaAt < 0 {
		t.Fatalf("answer/tool/metadata missing:\n%s", stripANSI(strings.Join(rows, "\n")))
	}
	if !(answerAt < toolAt && toolAt < metaAt) {
		t.Fatalf("order is answer=%d tool=%d meta=%d", answerAt, toolAt, metaAt)
	}
	if got := displayColumn(tool, "⚙ bash"); got != m.contentColumn() {
		t.Errorf("tool starts at %d, want %d", got, m.contentColumn())
	}
	if !strings.Contains(tool, "completed") || !strings.Contains(tool, "exit 0") ||
		!strings.Contains(tool, "666") {
		t.Errorf("tool line = %q", stripANSI(tool))
	}
	const metaText = "1s · 10 in / 20 out"
	if got, want := displayColumn(meta, metaText)+lipgloss.Width(metaText),
		m.contentColumn()+m.contentWidth(); got != want {
		t.Errorf("metadata right edge = %d, want %d (col=%d width=%d fragcol=%d fragw=%d row=%q)",
			got, want, m.contentColumn(), m.contentWidth(),
			displayColumn(meta, metaText), lipgloss.Width(metaText), stripANSI(meta))
	}
	if got := displayColumn(answer, "the answer is ready"); got != m.contentColumn() {
		t.Errorf("answer starts at %d, want %d", got, m.contentColumn())
	}
}

func TestTranscriptMarkdownStructuresStayInsideTheColumn(t *testing.T) {
	md := "## Heading\n\n" +
		"A paragraph with **strong** text and `code`.\n\n" +
		"- one item\n- two items\n\n" +
		"> quoted text\n\n" +
		"```go\nfunc main() {\n    fmt.Println(\"HAL\")\n}\n```\n\n" +
		"| Stage | State |\n|---|---|\n| Foundation | verified |\n| Polish | in progress |\n"
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}} {
		t.Run(strconv.Itoa(size[0]), func(t *testing.T) {
			m := transcriptModel(t, size[0], size[1])
			m.SetHistory([]SessionLine{{Role: "assistant", Text: md, TurnID: "t1"}})
			rows := allEntryRows(m)
			for _, row := range rows {
				if got := lipgloss.Width(row); got > size[0] {
					t.Errorf("%dx%d: row is %d cells: %q", size[0], size[1], got, stripANSI(row))
				}
			}
			plain := stripANSI(strings.Join(rows, "\n"))
			for _, want := range []string{"Heading", "paragraph", "one item", "quoted", "func main", "Foundation"} {
				if !strings.Contains(plain, want) {
					t.Errorf("%dx%d: markdown lost %q:\n%s", size[0], size[1], want, plain)
				}
			}
			_, heading := visibleRow(rows, "Heading")
			if heading != "" {
				if got := displayColumn(heading, "Heading"); got != m.contentColumn() {
					t.Errorf("heading starts at %d, want %d", got, m.contentColumn())
				}
			}
			_, code := visibleRow(rows, "func main")
			if code != "" {
				if got := displayColumn(code, "func main"); got != m.contentColumn() {
					t.Errorf("code starts at %d, want %d", got, m.contentColumn())
				}
			}
		})
	}
}

func TestToolActivityLineStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status string
		exit   *int
		mode   string
		want   []string
		avoid  []string
	}{
		{"success", "ok", intPointer(0), "666", []string{"completed", "exit 0", "666"}, []string{"failed"}},
		{"failure", "failed", intPointer(17), "666", []string{"failed", "exit 17", "666"}, []string{"completed"}},
		{"cancelled", "cancelled", nil, "666", []string{"cancelled", "666"}, []string{"completed"}},
		{"timeout", "timeout", nil, "666", []string{"timed out", "666"}, []string{"completed"}},
		{"refused", "denied", nil, "444", []string{"refused", "444"}, []string{"completed"}},
		{"running", "running", nil, "666", []string{"running", "666"}, []string{"completed", "failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := toolActivityLine(engineTool("bash", tc.status, tc.exit, tc.mode, ""), 60)
			for _, want := range tc.want {
				if !strings.Contains(line, want) {
					t.Errorf("line = %q, missing %q", line, want)
				}
			}
			for _, avoid := range tc.avoid {
				if strings.Contains(line, avoid) {
					t.Errorf("line = %q, must not contain %q", line, avoid)
				}
			}
			if got := lipgloss.Width(line); got > 60 {
				t.Errorf("line is %d cells: %q", got, line)
			}
		})
	}
}

func TestToolActivityLineTruncatesLongLabelsSafely(t *testing.T) {
	line := toolActivityLine(engineTool(
		"bash", "failed", intPointer(17), "666",
		"/a/very/long/target/path/that/should/be/shortened/without/losing/the/outcome",
	), 48)
	if got := lipgloss.Width(line); got > 48 {
		t.Fatalf("line is %d cells: %q", got, line)
	}
	for _, want := range []string{"failed", "exit 17", "666"} {
		if !strings.Contains(line, want) {
			t.Errorf("line = %q, missing %q", line, want)
		}
	}
}

func engineTool(name, status string, exit *int, mode, target string) engine.ToolActivity {
	a := engine.ToolActivity{
		Name: name, Action: name, Status: status, ExitCode: exit, Target: target,
	}
	if mode == "666" {
		a.Mode = permission.Unrestricted
	}
	return a
}

func TestTranscriptSelectionExcludesEyeToolsAndMetadata(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {170, 38}} {
		t.Run(strconv.Itoa(size[0]), func(t *testing.T) {
			m := transcriptModel(t, size[0], size[1])
			m.SetMouse(true)
			m.SetUsage(usage.Session{History: []usage.Turn{{Input: 10, Output: 20, Duration: 1000, Reported: true}}})
			m.SetHistory([]SessionLine{
				{Role: "user", Text: "run it", TurnID: "t1"},
				{
					Role: "assistant", Text: "the answer", TurnID: "t1",
					Tools: []session.ToolExecution{{
						TurnID: "t1", Tool: "bash", Mode: "666",
						Status: session.ToolSuccess, ExitCode: intPointer(0),
					}},
				},
			})
			m.renderConversation()
			last := m.conv.rows[len(m.conv.rows)-1]
			got := m.conv.extract(cellPos{row: 0, cell: 0}, cellPos{row: len(m.conv.rows) - 1, cell: last.width})
			if !strings.Contains(got, "run it") || !strings.Contains(got, "the answer") {
				t.Errorf("selection lost the conversation: %q", got)
			}
			for _, unwanted := range []string{"◉", "⚙", "666", " in / "} {
				if strings.Contains(got, unwanted) {
					t.Errorf("selection copied %q: %q", unwanted, got)
				}
			}
		})
	}
}

func TestThemeEyesAreRed(t *testing.T) {
	for _, theme := range Themes() {
		if theme.Eye == "" {
			t.Errorf("%s has no eye colour", theme.Name)
			continue
		}
		raw := strings.TrimPrefix(theme.Eye, "#")
		if len(raw) != 6 {
			t.Errorf("%s eye = %q", theme.Name, theme.Eye)
			continue
		}
		r, _ := strconv.ParseInt(raw[0:2], 16, 32)
		g, _ := strconv.ParseInt(raw[2:4], 16, 32)
		b, _ := strconv.ParseInt(raw[4:6], 16, 32)
		if r <= g || r <= b {
			t.Errorf("%s eye %q is not red", theme.Name, theme.Eye)
		}
	}
}
