package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rdx40/gmail-tui/api"
)

func TestSecurityDisplayText(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"normal", "日本語 café 😀\n\tindented", "日本語 café 😀\n\tindented"},
		{"CSI", "a\x1b[2Jb\x1b[31mc\x1b[0m", "abc"},
		{"clipboard", "a\x1b]52;c;YXR0YWNr\ab", "ab"},
		{"hyperlink", "\x1b]8;;https://evil\x1b\\label\x1b]8;;\x1b\\", "label"},
		{"string commands", "a\x1bPpayload\x1b\\b\x1b_secret\x1b\\c\x1b^secret\x1b\\d\x1bXsecret\x1b\\e", "abcde"},
		{"C1", "a\u009b2Jb\u009d52;c;secret\u009cc\u0090secret\u009cd\u0098secret\u009ce\u009esecret\u009cf\u009fsecret\u009cg", "abcdefg"},
		{"spoofing", "a\rb\bc\x00\x7f\u202e\u2066\u2069\u200f\u061c", "abc"},
		{"unterminated", "safe\x1b]52;c;secret", "safe"},
		{"escape intermediates", "a\x1b(Bb", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := displayLine("a\nb\tc"); got != "a b c" {
		t.Fatalf("single line = %q", got)
	}
}

func TestSecurityRenderingBoundaries(t *testing.T) {
	attack := "hello\x1b]52;c;payload\a\x1b[2J\u202e\r\b"
	email := api.Email{ID: "1", From: attack, To: attack, CC: attack, BCC: attack, Subject: attack, Date: attack, Snippet: attack, Body: attack,
		Attachments: []api.Attachment{{Filename: attack, Size: 1}}}
	m := newTestModel(&MockClient{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	updated, _ = m.Update(emailOpenedMsg{email: &email})
	m = updated.(Model)
	assertSafe := func(name, output string) {
		t.Helper()
		// App styling is permitted; reject the specific injected instructions.
		if strings.ContainsAny(output, "\a\r\b\u202e\u009d") || strings.Contains(output, "payload") || strings.Contains(output, "\x1b]52") || strings.Contains(output, "\x1b[2J") {
			t.Fatalf("%s leaked malicious content: %q", name, output)
		}
	}
	for _, noStyle := range []bool{true, false} {
		m.noStyle = noStyle
		assertSafe("opened email", m.View())
		m.downloading = true
		assertSafe("attachment picker", m.View())
		m.downloading = false
		m.replyTo = &email
		m.state = stateReplying
		m.replyAttachments = []string{attack}
		assertSafe("reply", m.View())
		m.state = stateViewing
		updated, _ = m.Update(errMsg{err: errors.New(attack)})
		m = updated.(Model)
		assertSafe("error status", m.View())
	}
	if email.Body != attack || email.Subject != attack {
		t.Fatal("display changed source message")
	}
	item := emailItem{email: email}
	for _, value := range []string{item.Title(), item.Description(), item.FilterValue(), (labelItem{label: api.Label{Name: attack}}).Title()} {
		assertSafe("delegate adapter", value)
	}
	m.state = stateInbox
	m.emailList.SetItems([]list.Item{item})
	assertSafe("inbox delegate", m.View())
	m.state = stateLabels
	m.labelList.SetItems(labelsToItems([]api.Label{{Name: attack}}))
	assertSafe("label delegate", m.View())
	updated, _ = m.Update(searchResultMsg{title: attack, emails: []api.Email{email}})
	m = updated.(Model)
	assertSafe("search title", m.View())
}

type readonlyMock struct{ MockClient }

func (*readonlyMock) HasModifyScope() bool { return false }

func TestSecurityReadOnlyUI(t *testing.T) {
	for _, state := range []viewState{stateInbox, stateViewing} {
		for _, k := range []string{"d", "a", "m", "c", "r"} {
			m := newTestModel(&readonlyMock{})
			m.state = state
			m.currentEmail = &api.Email{ID: "1"}
			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
			n := next.(Model)
			if n.confirmDelete || n.state == stateComposing || n.state == stateReplying {
				t.Fatalf("write action %s enabled in %v", k, state)
			}
			if cmd != nil {
				if _, ok := cmd().(statusMsg); !ok {
					t.Fatalf("write action %s scheduled API command", k)
				}
			}
		}
	}
}

func TestSecurityStatusPreservesAppFormatting(t *testing.T) {
	m := newTestModel(&MockClient{})
	m.noStyle = false
	m.statusMessage = "safe\x1b[2J"
	if got, want := m.statusBar(), "\n"+statusStyle.Render("safe")+"\n"; got != want {
		t.Fatalf("app formatting changed: %q, want %q", got, want)
	}
}
