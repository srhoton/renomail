package gmail

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gmailapi "google.golang.org/api/gmail/v1"
)

// loadFixtureMessage reads testdata/gmail_multipart.json into a gmail.Message.
func loadFixtureMessage(t *testing.T) *gmailapi.Message {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gmail_multipart.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var msg gmailapi.Message
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return &msg
}

func TestSelectBodies_multipartAlternative_prefersHTMLCapturesText(t *testing.T) {
	msg := loadFixtureMessage(t)
	htmlBody, textBody := selectBodies(msg.Payload)

	if !strings.Contains(htmlBody, "<b>HTML</b>") {
		t.Errorf("html body = %q, want the decoded text/html part", htmlBody)
	}
	if !strings.Contains(textBody, "Hello in plain text.") {
		t.Errorf("text body = %q, want the decoded text/plain fallback", textBody)
	}
}

func TestSelectBodies_nilPart_returnsEmpty(t *testing.T) {
	htmlBody, textBody := selectBodies(nil)
	if htmlBody != "" || textBody != "" {
		t.Errorf("selectBodies(nil) = (%q, %q), want empties", htmlBody, textBody)
	}
}

func TestSelectBodies_nestedMultipart_recurses(t *testing.T) {
	// multipart/mixed -> multipart/alternative -> {text/plain, text/html}.
	plain := base64.RawURLEncoding.EncodeToString([]byte("deep text"))
	htmlData := base64.RawURLEncoding.EncodeToString([]byte("<p>deep html</p>"))
	root := &gmailapi.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmailapi.MessagePart{{
			MimeType: "multipart/alternative",
			Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: plain}},
				{MimeType: "text/html", Body: &gmailapi.MessagePartBody{Data: htmlData}},
			},
		}},
	}
	htmlBody, textBody := selectBodies(root)
	if htmlBody != "<p>deep html</p>" || textBody != "deep text" {
		t.Errorf("nested recurse = (%q, %q)", htmlBody, textBody)
	}
}

func TestSelectBodies_textPartMissingBody_noPanic(t *testing.T) {
	root := &gmailapi.MessagePart{
		MimeType: "multipart/alternative",
		Parts: []*gmailapi.MessagePart{
			{MimeType: "text/html"},  // Body nil
			{MimeType: "text/plain"}, // Body nil
		},
	}
	htmlBody, textBody := selectBodies(root)
	if htmlBody != "" || textBody != "" {
		t.Errorf("missing bodies = (%q, %q), want empties", htmlBody, textBody)
	}
}

func TestSelectBodies_singlePartPlain(t *testing.T) {
	data := base64.RawURLEncoding.EncodeToString([]byte("just text"))
	root := &gmailapi.MessagePart{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: data}}
	htmlBody, textBody := selectBodies(root)
	if htmlBody != "" || textBody != "just text" {
		t.Errorf("single plain = (%q, %q)", htmlBody, textBody)
	}
}

func TestDecodeB64URL(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"unpadded", base64.RawURLEncoding.EncodeToString([]byte("hi there")), "hi there"},
		{"padded", base64.URLEncoding.EncodeToString([]byte("padded value")), "padded value"},
		{"malformed", "!!!not base64!!!", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeB64URL(tt.in); got != tt.want {
				t.Errorf("decodeB64URL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
