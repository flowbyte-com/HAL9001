package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// The reasoning a provider exposes can be very long, and it arrives in
// small pieces. The interface must not spend more on a chunk than the
// chunk costs: the block is wrapped and styled once, as it arrives, and
// the rows that are already on screen never move.
//
// The buffer therefore keeps three things apart: the text as the
// provider sent it, the rows it was wrapped into, and the row that is
// still being built.

// The bounds of the display.
//
// They bound what is *shown*, never what the provider sent: the
// reasoning a provider needs for its next request lives in the
// conversation record, not here.
const (
	// maxThinkingBytes bounds the reasoning kept for a re-wrap.
	maxThinkingBytes = 512 << 10
	// maxThinkingRows bounds the rows kept on screen.
	//
	// The bound is what keeps the interface responsive: the transcript
	// is measured again every time a piece of reasoning arrives, so a
	// block that grew without limit would make every piece cost more
	// than the one before it.
	//
	// The window holds the newest reasoning: it is what the operator
	// reads, and the marker above it says that the rest is not shown.
	maxThinkingRows = 256
	// thinkingDropNotice says that the beginning of the reasoning is no
	// longer shown.
	thinkingDropNotice = "… earlier reasoning is not shown"
)

// thinkingBuffer holds the reasoning of one turn and renders it once.
//
// The reasoning is wrapped as it arrives. A word is held back until it
// ends, because a word that is still growing may not fit where it is:
// only the row under construction changes, and the rows above it stay
// where the operator read them.
//
// The layout it produces is the layout a whole-text wrap produces: the
// same text, wrapped the same way, whether it arrived in one piece or in
// a thousand.
type thinkingBuffer struct {
	// width is the width the reasoning is wrapped to.
	width int
	// rows are the finished rows of the block, unwrapped.
	rows []string
	// row is the row under construction.
	row      string
	rowWidth int
	// word is the last piece of the word under construction.
	word      string
	wordWidth int
	// wordTotal is the width of the whole word, the pieces already
	// broken off it included.
	wordTotal int
	// pieces are the rows a word too long for the width was broken
	// into. They belong to the word under construction and are shown
	// above its last piece.
	pieces []string
	// text is the reasoning as the provider sent it.
	text []byte
	// dropped reports that the beginning of the reasoning is no longer
	// kept.
	dropped bool
	// style turns a row into the row on screen.
	style func(string) string
	// styleKey names the style the rows were made with.
	styleKey string
	// styled is the styled form of rows, kept beside them so a change
	// of style does not need the text again.
	styled []string
	// styledPieces is the styled form of pieces.
	styledPieces []string
	// done reports that the reasoning has ended: there is no row under
	// construction any more.
	done bool
}

// newThinkingBuffer returns a buffer for the reasoning of one turn.
func newThinkingBuffer(width int, style func(string) string, styleKey string) *thinkingBuffer {
	if width < 1 {
		width = 1
	}
	return &thinkingBuffer{width: width, style: style, styleKey: styleKey}
}

// Append takes the next piece of the reasoning.
//
// It costs what the piece costs: the rows that are already on screen
// are not touched, and only the row under construction is rebuilt.
func (b *thinkingBuffer) Append(text string) {
	if text == "" {
		return
	}
	b.done = false
	b.text = append(b.text, text...)
	b.trimText()
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		cluster := graphemes.Str()
		r, _ := utf8.DecodeRuneInString(cluster)
		switch {
		case r == '\n':
			b.endWord()
			b.endRow()
		case unicode.IsSpace(r):
			b.endWord()
		default:
			b.addCluster(cluster, graphemes.Width())
		}
	}
}

// Flush ends the reasoning.
//
// The last row is finished and shown: a reasoning that ended in the
// middle of a word is not left half a word short.
func (b *thinkingBuffer) Flush() {
	if len(b.text) == 0 {
		b.done = true
		return
	}
	b.endWord()
	b.endRow()
	b.done = true
}

// Text returns the reasoning as the provider sent it.
func (b *thinkingBuffer) Text() string { return string(b.text) }

// Empty reports whether there is any reasoning to show.
//
// Reasoning that is only whitespace shows nothing: a provider that
// exposed no reasoning shows no block, because a placeholder would be
// text HAL made up.
func (b *thinkingBuffer) Empty() bool { return strings.TrimSpace(string(b.text)) == "" }

// Rows returns the rows the reasoning is shown as.
//
// The rows that are finished are returned as they were made, and the
// row under construction is the last one.
func (b *thinkingBuffer) Rows() []string {
	out := make([]string, 0, len(b.rows)+len(b.pieces)+1)
	if b.dropped {
		out = append(out, b.style(thinkingDropNotice))
	}
	out = append(out, b.styled...)
	out = append(out, b.styledPieces...)
	if !b.done {
		out = append(out, b.style(b.provisional()))
	}
	return out
}

// SetWidth re-wraps the reasoning for another width.
//
// It is called when the window changes, not when the reasoning arrives:
// the reasoning on screen is wrapped once per width, not once per chunk.
func (b *thinkingBuffer) SetWidth(width int) {
	if width < 1 {
		width = 1
	}
	if width == b.width {
		return
	}
	text := string(b.text)
	b.width = width
	b.reset()
	b.text = nil
	b.Append(text)
	b.Flush()
}

// SetStyle changes how the rows are styled.
//
// The rows are styled again only when the style itself changed: a theme
// change restyles the block, nothing else does.
func (b *thinkingBuffer) SetStyle(style func(string) string, key string) {
	if style == nil {
		style = func(line string) string { return line }
	}
	if b.styleKey == key && b.style != nil {
		b.style = style
		return
	}
	b.style = style
	b.styleKey = key
	b.styled = b.styled[:0]
	b.styledPieces = b.styledPieces[:0]
	for _, row := range b.rows {
		b.styled = append(b.styled, b.style(row))
	}
	for _, piece := range b.pieces {
		b.styledPieces = append(b.styledPieces, b.style(piece))
	}
}

// addCluster adds one glyph to the word under construction.
func (b *thinkingBuffer) addCluster(cluster string, width int) {
	if b.row != "" && b.rowWidth+1+b.wordTotal+width > b.width {
		// The word does not fit on this row any more: the row ends
		// here and the word starts the next one.
		b.endRow()
	}
	if b.word != "" && b.wordWidth+width > b.width {
		// The word is longer than a row: it is broken, and the piece
		// that is full is a row of its own.
		b.pieces = append(b.pieces, b.word)
		b.styledPieces = append(b.styledPieces, b.style(b.word))
		b.word = ""
		b.wordWidth = 0
		b.trimPieces()
	}
	if b.word == "" && width > b.width {
		// A glyph wider than the row is a row of its own, and it is
		// the piece the word continues with.
		b.word = cluster
		b.wordWidth = width
		b.wordTotal += width
		return
	}
	b.word += cluster
	b.wordWidth += width
	b.wordTotal += width
}

// endWord places the word under construction.
//
// The word joins the row when it fits there, and starts a row of its
// own when it does not.
func (b *thinkingBuffer) endWord() {
	if b.word == "" && b.wordTotal == 0 {
		return
	}
	if b.row == "" {
		// The word starts a row. The pieces it was broken into are
		// shown above its last piece.
		b.rows = append(b.rows, b.pieces...)
		b.styled = append(b.styled, b.styledPieces...)
		b.pieces = nil
		b.styledPieces = nil
		b.row = b.word
		b.rowWidth = b.wordWidth
	} else {
		b.row += " " + b.word
		b.rowWidth += 1 + b.wordWidth
	}
	b.word = ""
	b.wordWidth = 0
	b.wordTotal = 0
}

// endRow finishes the row under construction.
func (b *thinkingBuffer) endRow() {
	b.rows = append(b.rows, b.row)
	b.styled = append(b.styled, b.style(b.row))
	b.row = ""
	b.rowWidth = 0
	b.trimRows()
}

// provisional returns the row under construction, as it is shown while
// the reasoning is still arriving.
func (b *thinkingBuffer) provisional() string {
	switch {
	case b.row != "" && b.word != "":
		return b.row + " " + b.word
	case b.row != "":
		return b.row
	default:
		return b.word
	}
}

// trimRows keeps the block within its bound.
//
// The oldest rows go first: the reasoning a provider streams is read
// from the top, and the operator has already read them.
func (b *thinkingBuffer) trimRows() {
	if len(b.rows) <= maxThinkingRows {
		return
	}
	// Rows are dropped in batches: dropping one at a time would make
	// the work of a chunk grow with the block.
	drop := maxThinkingRows / 4
	if drop < 1 {
		drop = 1
	}
	if drop > len(b.rows) {
		drop = len(b.rows)
	}
	b.rows = append(b.rows[:0], b.rows[drop:]...)
	b.styled = append(b.styled[:0], b.styled[drop:]...)
	b.dropped = true
}

// trimPieces keeps a word that is longer than the screen within its
// bound.
//
// The pieces of a word are its continuation: the oldest ones are the
// first the operator read.
func (b *thinkingBuffer) trimPieces() {
	if len(b.pieces) <= maxThinkingRows {
		return
	}
	drop := maxThinkingRows / 4
	if drop < 1 {
		drop = 1
	}
	if drop > len(b.pieces) {
		drop = len(b.pieces)
	}
	b.pieces = append(b.pieces[:0], b.pieces[drop:]...)
	b.styledPieces = append(b.styledPieces[:0], b.styledPieces[drop:]...)
	b.dropped = true
}

// trimText keeps the reasoning within its bound.
//
// The text is kept for a re-wrap only. What the provider needs for its
// next request lives in the conversation record, which has its own
// bound and its own rules.
func (b *thinkingBuffer) trimText() {
	if len(b.text) <= maxThinkingBytes {
		return
	}
	keep := b.text[len(b.text)-maxThinkingBytes:]
	// The cut is made at a word boundary, so the reasoning that is
	// kept starts at the beginning of a word.
	if cut := strings.IndexAny(string(keep), " \n\t"); cut >= 0 && cut < len(keep)/2 {
		keep = keep[cut+1:]
	}
	b.text = append(b.text[:0], keep...)
	b.dropped = true
}

// reset empties the block, keeping the style and the width.
func (b *thinkingBuffer) reset() {
	b.rows = nil
	b.styled = nil
	b.pieces = nil
	b.styledPieces = nil
	b.row = ""
	b.rowWidth = 0
	b.word = ""
	b.wordWidth = 0
	b.wordTotal = 0
	b.done = false
}
