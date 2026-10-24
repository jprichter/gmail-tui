package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("196")).
			Padding(0, 1)
)

// View renders the current state.
func (m Model) View() string {
	if m.showHelp {
		return m.help.View(keys)
	}

	switch m.state {
	case stateLoading:
		return m.loadingView()
	case stateInbox:
		return m.inboxView()
	case stateViewing:
		return m.emailView()
	case stateComposing:
		return m.composeView()
	case stateReplying:
		return m.replyView()
	case stateSearching:
		return m.searchView()
	case stateLabels:
		return m.labelsView()
	}
	return ""
}

func (m Model) statusBar() string {
	if m.statusMessage == "" {
		return ""
	}
	if m.noStyle {
		prefix := ""
		if strings.HasPrefix(m.statusMessage, "Error:") {
			prefix = "[ERROR] "
		}
		return "\n" + prefix + displayLine(m.statusMessage) + "\n"
	}
	s := statusStyle
	if strings.HasPrefix(m.statusMessage, "Error:") {
		s = errorStyle
	}
	return "\n" + s.Render(displayLine(m.statusMessage)) + "\n"
}

func (m Model) loadingView() string {
	if m.noStyle {
		return m.spinner.View() + " Loading...\n"
	}
	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, m.spinner.View(), "Loading..."),
	)
}

func (m Model) inboxView() string {
	pageInfo := fmt.Sprintf("Page %d", m.currentPage)
	if m.totalFetched > 0 {
		pageInfo = fmt.Sprintf("Page %d • %d emails", m.currentPage, m.totalFetched)
	}
	help := fmt.Sprintf("\n[c] compose • [d] delete • [a] archive • [m] read/unread • [l] labels • [/] search • [R] refresh • [n/p] page • [?] help • [q] quit  %s\n", pageInfo)
	return m.emailList.View() + m.statusBar() + help
}

func (m Model) emailView() string {
	var b strings.Builder

	fmt.Fprintf(&b, "\nFrom: %s\n", displayLine(m.currentEmail.From))
	fmt.Fprintf(&b, "To: %s\n", displayLine(m.currentEmail.To))
	if m.currentEmail.CC != "" {
		fmt.Fprintf(&b, "CC: %s\n", displayLine(m.currentEmail.CC))
	}
	if m.currentEmail.BCC != "" {
		fmt.Fprintf(&b, "BCC: %s\n", displayLine(m.currentEmail.BCC))
	}
	fmt.Fprintf(&b, "Subject: %s\n", displayLine(m.currentEmail.Subject))
	fmt.Fprintf(&b, "Date: %s\n\n", displayLine(m.currentEmail.Date))
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	if m.downloading {
		b.WriteString("\nDownload which attachment? (1-9) [esc] cancel\n")
		for i, att := range m.currentEmail.Attachments {
			if i < 9 {
				fmt.Fprintf(&b, "  [%d] %s (%s)\n", i+1, displayLine(att.Filename), humanSize(att.Size))
			}
		}
	} else if len(m.currentEmail.Attachments) > 0 {
		b.WriteString("\nAttachments:\n")
		for i, att := range m.currentEmail.Attachments {
			fmt.Fprintf(&b, "  [%d] %s (%s)\n", i+1, displayLine(att.Filename), humanSize(att.Size))
		}
	}

	b.WriteString(m.statusBar())
	b.WriteString("\n[esc] back • [r] reply • [d] delete • [a] archive • [m] read/unread • [ctrl+d] download attachment • [q] quit\n")
	return b.String()
}

// fieldLabel returns a label with a focus indicator for the compose form.
func fieldLabel(focused int, idx int, label string) string {
	if focused == idx {
		return "▶ " + label + ":"
	}
	return "  " + label + ":"
}

func (m Model) composeView() string {
	var b strings.Builder

	b.WriteString("\n  Compose New Email\n\n")
	fmt.Fprintf(&b, "%s %s\n", fieldLabel(m.compose.focused, 0, "From"), m.compose.from.View())
	fmt.Fprintf(&b, "%s %s\n", fieldLabel(m.compose.focused, 1, "To  "), m.compose.to.View())
	fmt.Fprintf(&b, "%s %s\n", fieldLabel(m.compose.focused, 2, "CC  "), m.compose.cc.View())
	fmt.Fprintf(&b, "%s %s\n", fieldLabel(m.compose.focused, 3, "BCC "), m.compose.bcc.View())
	fmt.Fprintf(&b, "%s %s\n\n", fieldLabel(m.compose.focused, 4, "Subj"), m.compose.subject.View())

	bodyLabel := "  Body"
	if m.compose.focused == 5 {
		bodyLabel = "▶ Body"
	}
	fmt.Fprintf(&b, "%s (%d chars):\n", bodyLabel, len(m.compose.body.Value()))
	b.WriteString(m.compose.body.View() + "\n")

	if len(m.compose.attachments) > 0 {
		b.WriteString("\nAttachments:\n")
		for i, f := range m.compose.attachments {
			fmt.Fprintf(&b, "  [%d] %s\n", i+1, displayLine(filepath.Base(f)))
		}
	}

	if m.compose.addingAttach {
		b.WriteString("\nAttachment Path: " + m.compose.attachInput.View())
	}

	b.WriteString(m.statusBar())
	b.WriteString("\n[ctrl+s] send • [ctrl+a] add attachment • [ctrl+x] remove attachment • [tab] next field • [esc] back")
	return b.String()
}

func (m Model) replyView() string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n  Reply to: %s\n", displayLine(m.replyTo.From))
	fmt.Fprintf(&b, "  Subject: %s\n\n", displayLine(formatReplySubject(m.replyTo.Subject)))
	b.WriteString(m.replyBody.View() + "\n")

	if len(m.replyAttachments) > 0 {
		b.WriteString("\nAttachments:\n")
		for i, f := range m.replyAttachments {
			fmt.Fprintf(&b, "  [%d] %s\n", i+1, displayLine(filepath.Base(f)))
		}
	}

	if m.addingAttach {
		b.WriteString("\nAttachment Path: " + m.attachInput.View())
	}

	b.WriteString(m.statusBar())
	b.WriteString("\n[ctrl+s] send • [ctrl+a] add attachment • [ctrl+x] remove attachment • [esc] back")
	return b.String()
}

func (m Model) searchView() string {
	return "\n  Search: " + m.searchInput.View() + "\n\n[enter] search • [esc] cancel\n"
}

func (m Model) labelsView() string {
	return m.labelList.View() + "\n[j/k] navigate • [enter] select • [esc/b] back\n"
}

// humanSize formats a byte count as a human-readable string.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
