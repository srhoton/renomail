package filterbar

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNew_hasPrompt(t *testing.T) {
	m := New()
	if !strings.Contains(m.View(), prompt) {
		t.Errorf("View() = %q, want it to contain prompt %q", m.View(), prompt)
	}
	if m.Focused() {
		t.Error("New() returned a focused bar, want unfocused")
	}
	if m.Value() != "" {
		t.Errorf("New() Value = %q, want empty", m.Value())
	}
}

func TestFocus_focusesAndReturnsCmd(t *testing.T) {
	m := New()
	cmd := m.Focus()
	if cmd == nil {
		t.Error("Focus() returned nil cmd, want the blink command")
	}
	if !m.Focused() {
		t.Error("Focused() = false after Focus()")
	}
	m.Blur()
	if m.Focused() {
		t.Error("Focused() = true after Blur()")
	}
}

func TestSetValueValue_roundTrips(t *testing.T) {
	m := New()
	m.SetValue("kubernetes")
	if m.Value() != "kubernetes" {
		t.Errorf("Value() = %q, want %q", m.Value(), "kubernetes")
	}
	m.SetValue("")
	if m.Value() != "" {
		t.Errorf("Value() = %q after clearing, want empty", m.Value())
	}
}

func TestUpdate_typingAppendsToValue(t *testing.T) {
	m := New()
	m.Focus() // input must be focused to accept keystrokes
	for _, r := range "go" {
		var cmd tea.Cmd
		m, cmd = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		_ = cmd
	}
	if m.Value() != "go" {
		t.Errorf("Value() = %q after typing, want %q", m.Value(), "go")
	}
}

func TestView_showsTypedValue(t *testing.T) {
	m := New()
	m.SetValue("rust")
	if got := m.View(); !strings.Contains(got, "rust") {
		t.Errorf("View() = %q, want it to contain the value", got)
	}
}
