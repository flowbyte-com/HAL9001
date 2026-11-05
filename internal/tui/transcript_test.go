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
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}, {200, 45}} {
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
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}, {170, 38}, {200, 45}} {
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
	for _, size := range [][2]int{{80, 24}, {120, 30}, {170, 38}, {200, 45}} {
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

func TestTranscriptUsesFullUsableWidth(t *testing.T) {
	for _, width := range []int{60, 80, 120, 170, 200} {
		m := transcriptModel(t, width, 45)
		if got := m.contentColumn(); got != 2 {
			t.Errorf("%d: content column = %d, want 2", width, got)
		}
		if got, want := m.contentWidth(), width-4; got != want {
			t.Errorf("%d: content width = %d, want %d", width, got, want)
		}
		m.SetUsage(usage.Session{History: []usage.Turn{{
			Input: 10, Output: 20, Duration: 1000, Reported: true,
		}}})
		m.SetHistory([]SessionLine{
			{Role: "user", Text: "hello", TurnID: "t1"},
			{Role: "assistant", Text: "the answer", TurnID: "t1"},
		})
		rows := allEntryRows(m)
		_, answer := visibleRow(rows, "the answer")
		if answer == "" {
			t.Fatalf("%d: answer missing", width)
		}
		if got := displayColumn(answer, "the answer"); got != 2 {
			t.Errorf("%d: answer starts at %d, want 2", width, got)
		}
		last := m.entries[len(m.entries)-1].rows
		if len(last) == 0 {
			t.Fatalf("%d: empty assistant", width)
		}
		if got, want := lipgloss.Width(last[len(last)-1]), width-2; got != want {
			t.Errorf("%d: statistics row ends at %d, want %d: %q",
				width, got, want, stripANSI(last[len(last)-1]))
		}
	}
}

func rowIndex(rows []string, fragment string) int {
	for i, row := range rows {
		if strings.Contains(stripANSI(row), fragment) {
			return i
		}
	}
	return -1
}

func blankRowsBetween(rows []string, first, last int) int {
	if first < 0 || last <= first {
		return -1
	}
	n := 0
	for i := first + 1; i < last; i++ {
		if strings.TrimSpace(stripANSI(rows[i])) == "" {
			n++
		}
	}
	return n
}

func TestTranscriptVerticalRhythm(t *testing.T) {
	m := transcriptModel(t, 80, 45)
	m.SetUsage(usage.Session{History: []usage.Turn{
		{Input: 10, Output: 20, Duration: 1000, Reported: true},
		{Input: 30, Output: 40, Duration: 2500, Reported: true},
	}})
	markdown := "First paragraph.\n\n" +
		"Second paragraph.\n\n" +
		"- first item\n- second item\n- third item\n\n" +
		"```go\nfmt.Println(\"HAL\")\n```\n\n" +
		"| Stage | State |\n|---|---|\n| Foundation | verified |\n"
	m.SetHistory([]SessionLine{
		{Role: "user", Text: "first question", TurnID: "t1"},
		{Role: "assistant", Text: markdown, TurnID: "t1"},
		{Role: "user", Text: "second question", TurnID: "t2"},
		{
			Role: "assistant", Text: "answer text", TurnID: "t2",
			Tools: []session.ToolExecution{
				{TurnID: "t2", Tool: "write", Mode: "666", Status: session.ToolSuccess},
				{Tool: "bash", Mode: "666", Status: session.ToolFailure, ExitCode: intPointer(17)},
				{Tool: "bash", Mode: "666", Status: session.ToolCancelled},
			},
		},
	})
	if len(m.entries) != 4 {
		t.Fatalf("entries = %d", len(m.entries))
	}
	body := m.entries[1].rows
	if len(body) == 0 || strings.TrimSpace(stripANSI(body[0])) == "" ||
		!strings.Contains(stripANSI(body[0]), "◉") {
		t.Fatalf("assistant block starts without the eye: %#v", body)
	}
	if !strings.Contains(stripANSI(body[len(body)-1]), "1s · 10 in / 20 out") {
		t.Fatalf("assistant block ends with %q, want statistics", stripANSI(body[len(body)-1]))
	}
	firstParagraph := rowIndex(body, "First paragraph")
	secondParagraph := rowIndex(body, "Second paragraph")
	if got := blankRowsBetween(body, firstParagraph, secondParagraph); got != 1 {
		t.Errorf("paragraph gap = %d blank rows, want 1", got)
	}
	firstItem := rowIndex(body, "first item")
	secondItem := rowIndex(body, "second item")
	thirdItem := rowIndex(body, "third item")
	if got := blankRowsBetween(body, firstItem, secondItem); got != 0 {
		t.Errorf("first list gap = %d blank rows, want 0", got)
	}
	if got := blankRowsBetween(body, secondItem, thirdItem); got != 0 {
		t.Errorf("second list gap = %d blank rows, want 0", got)
	}
	code := rowIndex(body, "fmt.Println")
	if got := blankRowsBetween(body, thirdItem, code); got != 1 {
		t.Errorf("prose/code gap = %d blank rows, want 1", got)
	}
	table := rowIndex(body, "Stage")
	if got := blankRowsBetween(body, code, table); got != 1 {
		t.Errorf("code/table gap = %d blank rows, want 1", got)
	}
	meta := rowIndex(body, "1s · 10 in / 20 out")
	if got := blankRowsBetween(body, table, meta); got != 0 {
		t.Errorf("table/statistics gap = %d blank rows, want 0", got)
	}

	tools := m.entries[3].rows
	if len(tools) == 0 || !strings.Contains(stripANSI(tools[0]), "◉") {
		t.Fatalf("second answer starts without the eye: %#v", tools)
	}
	answer := rowIndex(tools, "answer text")
	firstTool := -1
	toolRows := make([]int, 0, 3)
	for i, row := range tools {
		if strings.HasPrefix(strings.TrimSpace(stripANSI(row)), "⚙") {
			toolRows = append(toolRows, i)
			if firstTool < 0 {
				firstTool = i
			}
		}
	}
	if len(toolRows) != 3 {
		t.Fatalf("tool rows = %d, want 3: %#v", len(toolRows), tools)
	}
	if got := blankRowsBetween(tools, answer, firstTool); got != 1 {
		t.Errorf("answer/tool gap = %d blank rows, want 1", got)
	}
	for i := 1; i < len(toolRows); i++ {
		if got := blankRowsBetween(tools, toolRows[i-1], toolRows[i]); got != 0 {
			t.Errorf("tool row gap %d = %d blank rows, want 0", i, got)
		}
	}
	secondMeta := rowIndex(tools, "2s · 30 in / 40 out")
	if got := blankRowsBetween(tools, toolRows[len(toolRows)-1], secondMeta); got != 0 {
		t.Errorf("tool/statistics gap = %d blank rows, want 0", got)
	}
	if strings.TrimSpace(stripANSI(tools[len(tools)-1])) == "" {
		t.Error("assistant block ends with a blank row")
	}

	conversation := strings.Split(m.viewport.GetContent(), "\n")
	q1 := rowIndex(conversation, "first question")
	p1 := rowIndex(conversation, "First paragraph")
	if got := blankRowsBetween(conversation, q1, p1); got != 1 {
		t.Errorf("first turn gap = %d blank rows, want 1", got)
	}
	m1 := rowIndex(conversation, "1s · 10 in / 20 out")
	q2 := rowIndex(conversation, "second question")
	if got := blankRowsBetween(conversation, m1, q2); got != 1 {
		t.Errorf("second turn gap = %d blank rows, want 1", got)
	}
}

func TestTranscriptResizeRecomputesWidthAndWrapping(t *testing.T) {
	m := transcriptModel(t, 170, 38)
	m.SetUsage(usage.Session{History: []usage.Turn{{
		Input: 10, Output: 20, Duration: 1000, Reported: true,
	}}})
	long := strings.Repeat("responsive word ", 30)
	m.SetHistory([]SessionLine{
		{Role: "user", Text: "resize me", TurnID: "t1"},
		{Role: "assistant", Text: long, TurnID: "t1"},
	})
	largeRows := len(m.entries[1].rows)
	if got := m.contentWidth(); got != 166 {
		t.Fatalf("170 width = %d, want 166", got)
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	smallRows := len(m.entries[1].rows)
	if got := m.contentWidth(); got != 76 {
		t.Fatalf("80 width = %d, want 76", got)
	}
	if smallRows <= largeRows {
		t.Errorf("wrapping did not change: %d -> %d rows", largeRows, smallRows)
	}
	for _, row := range m.entries[1].rows {
		if lipgloss.Width(row) > 80 {
			t.Errorf("80-column row overflows: %q", stripANSI(row))
		}
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 170, Height: 38})
	m = updated.(Model)
	if got := m.contentWidth(); got != 166 {
		t.Fatalf("back to 170: width = %d, want 166", got)
	}
	if got := len(m.entries[1].rows); got != largeRows {
		t.Errorf("rows after round-trip = %d, want %d", got, largeRows)
	}
	last := m.entries[1].rows
	if got := lipgloss.Width(last[len(last)-1]); got != 168 {
		t.Errorf("statistics end = %d, want 168", got)
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
