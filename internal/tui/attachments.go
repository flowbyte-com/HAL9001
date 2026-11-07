package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/attachments"
)

// AttachmentStore is the operator-facing ingestion surface: it copies a
// chosen file into HAL's upload directory and returns the durable
// reference to the copy.
//
// The interface is deliberately tiny. The interface is the only place
// where an ingestion is ever started, and it is started only by the
// operator, through the /attach command. No model-facing tool reaches
// it, so the model can never choose a file for itself.
type AttachmentStore interface {
	Attach(path string) (attachments.Meta, error)
	// Size reports the size of a file without copying it, so a file
	// that would not fit can be refused before a copy is made.
	Size(path string) (int64, error)
	// DisplayPath reports where the copies live, the way the interface
	// shows it to the operator.
	DisplayPath() string
}

// attachPaperclip is the marker of an attached file. It is the same
// two-cell glyph everywhere it appears, so the line stays aligned.
const attachPaperclip = "📎"

// attachmentSummary renders the attached files as one line: the name and
// the size of each, separated by a middle dot.
func attachmentSummary(files []attachments.Meta) string {
	if len(files) == 0 {
		return ""
	}
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, f.Display())
	}
	return attachPaperclip + " " + strings.Join(parts, " · ")
}

// attachmentNames renders just the names, for the short list form.
func attachmentNames(files []attachments.Meta) string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}

// attachmentRow is the line that sits directly above the composer while
// files are pending. It names the files and their sizes, so the
// operator can see what the next message will carry.
func (m Model) attachmentRow() string {
	text := attachmentSummary(m.pendingFiles)
	if text == "" {
		return ""
	}
	text = truncateCells(text, m.width)
	if !m.color {
		return text
	}
	return m.theme.styles().footer.Render(text)
}

// attachFile copies one chosen file into the upload directory and adds
// it to the pending list.
func (m *Model) attachFile(raw string) {
	if m.attachStore == nil {
		m.notice = "Attachments are unavailable in this session."
		return
	}
	path := cleanAttachPath(raw)
	if path == "" {
		m.notice = "Usage: /attach <path>"
		return
	}
	if len(m.pendingFiles) >= attachments.MaxFilesPerMessage {
		m.notice = m.noticeLine(fmt.Sprintf("At most %d files can be attached to one message.",
			attachments.MaxFilesPerMessage))
		return
	}
	// The size is checked before the copy is made, so a file that would
	// not fit leaves nothing behind.
	size, err := m.attachStore.Size(path)
	if err != nil {
		m.notice = m.noticeLine(attachFailure(err, path))
		return
	}
	if total := m.pendingBytes() + size; total > attachments.MaxPendingBytes {
		m.notice = m.noticeLine(fmt.Sprintf("The attached files would be %s together; the limit is %s.",
			attachments.HumanSize(total), attachments.HumanSize(attachments.MaxPendingBytes)))
		return
	}
	meta, err := m.attachStore.Attach(path)
	if err != nil {
		m.notice = m.noticeLine(attachFailure(err, path))
		return
	}
	m.pendingFiles = append(m.pendingFiles, meta)
	m.resizeFrame()
	m.notice = m.noticeLine(fmt.Sprintf("Attached %s", meta.Display()))
}

// listAttachments reports the files that are pending for the next
// message.
func (m *Model) listAttachments() {
	if len(m.pendingFiles) == 0 {
		m.notice = "No files are attached."
		return
	}
	m.notice = m.noticeLine(fmt.Sprintf("%d attached: %s · %s",
		len(m.pendingFiles), attachmentNames(m.pendingFiles), m.uploadRoot()))
}

// detachFile removes one pending attachment.
//
// The attachment is named by its identity or by its filename. The copy
// stays in the upload directory: removing an attachment from the message
// is not a request to destroy the operator's file.
func (m *Model) detachFile(raw string) {
	id := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
	if id == "" {
		m.notice = "Usage: /detach <name>"
		return
	}
	idx := m.findPending(id)
	if idx < 0 {
		m.notice = m.noticeLine(fmt.Sprintf("No pending attachment matches %q.", id))
		return
	}
	meta := m.pendingFiles[idx]
	m.pendingFiles = append(m.pendingFiles[:idx], m.pendingFiles[idx+1:]...)
	m.resizeFrame()
	m.notice = m.noticeLine(fmt.Sprintf("Detached %s — the copy stays in %s",
		meta.Name, m.uploadRoot()))
}

// findPending locates one pending attachment by identity or by
// filename. It returns -1 when the name is unknown or ambiguous.
//
// An unambiguous prefix of the identity is enough: the identity is
// random, so a few characters already single one file out.
func (m Model) findPending(name string) int {
	for i, f := range m.pendingFiles {
		if strings.EqualFold(f.ID, name) || strings.EqualFold(f.Name, name) {
			return i
		}
	}
	match := -1
	matches := 0
	for i, f := range m.pendingFiles {
		if strings.HasPrefix(strings.ToLower(f.ID), strings.ToLower(name)) {
			match = i
			matches++
		}
	}
	if matches != 1 {
		return -1
	}
	return match
}

// pendingBytes is the total size of the files pending for the next
// message.
func (m Model) pendingBytes() int64 {
	var total int64
	for _, f := range m.pendingFiles {
		total += f.Bytes
	}
	return total
}

// uploadRoot is where the copies live, as the interface shows it.
func (m Model) uploadRoot() string {
	if m.attachStore == nil {
		return ""
	}
	return m.attachStore.DisplayPath()
}

// cleanAttachPath prepares the operator's path: a quoted argument keeps
// its spaces, an unquoted one is taken exactly as typed.
func cleanAttachPath(raw string) string {
	path := strings.TrimSpace(raw)
	if len(path) >= 2 {
		if (strings.HasPrefix(path, `"`) && strings.HasSuffix(path, `"`)) ||
			(strings.HasPrefix(path, "'") && strings.HasSuffix(path, "'")) {
			path = path[1 : len(path)-1]
		}
	}
	return strings.TrimSpace(path)
}

// noticeLine shortens a notice to the terminal width.
//
// A notice is one line under the footer. A long path must never push the
// frame out of shape, so the text is cut rather than wrapped.
func (m Model) noticeLine(text string) string {
	return truncateCells(text, m.width)
}

// attachFailure turns a failure into one line the operator can act on.
func attachFailure(err error, path string) string {
	switch {
	case errors.Is(err, attachments.ErrMissing):
		return "There is no such file: " + path
	case errors.Is(err, attachments.ErrDirectory):
		return "That is a directory, not a file: " + path
	case errors.Is(err, attachments.ErrTooLarge):
		return "That file is too large to attach: " + path
	case errors.Is(err, attachments.ErrBinary):
		return "That file is binary; HAL attaches text files: " + path
	case errors.Is(err, attachments.ErrEncoding):
		return "HAL could not decode that file's text encoding: " + path
	case errors.Is(err, attachments.ErrNotRegular):
		return "That is not a regular file: " + path
	case errors.Is(err, attachments.ErrSymlink):
		return "The upload directory is a symbolic link; HAL will not write through it."
	case errors.Is(err, attachments.ErrChanged):
		return "That file changed while it was being attached; try again."
	default:
		return "Could not attach that file: " + err.Error()
	}
}
