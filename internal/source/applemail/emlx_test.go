package applemail

import (
	"strconv"
	"strings"
	"testing"
)

// wrapEmlx prepends the .emlx decimal length line and appends a trailing plist, as
// the real container does.
func wrapEmlx(rfc822 string) []byte {
	return []byte(strconv.Itoa(len(rfc822)) + "\n" + rfc822 + "<?xml?><plist></plist>")
}

func TestParseEmlx_multipartAlternative(t *testing.T) {
	rfc822 := "Subject: Hi\r\nContent-Type: multipart/alternative; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nplain part\r\n" +
		"--B\r\nContent-Type: text/html\r\n\r\n<b>html part</b>\r\n" +
		"--B--\r\n"

	html, text, err := parseEmlx(wrapEmlx(rfc822))
	if err != nil {
		t.Fatalf("parseEmlx: %v", err)
	}
	if !strings.Contains(text, "plain part") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(html, "html part") {
		t.Errorf("html = %q", html)
	}
}

func TestParseEmlx_quotedPrintableAndBase64(t *testing.T) {
	rfc822 := "Content-Type: multipart/alternative; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"caf=C3=A9 time=\r\n\r\n" +
		"--B\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"PGI+aGk8L2I+\r\n" + // <b>hi</b>
		"--B--\r\n"

	html, text, err := parseEmlx(wrapEmlx(rfc822))
	if err != nil {
		t.Fatalf("parseEmlx: %v", err)
	}
	if !strings.Contains(text, "café time") {
		t.Errorf("quoted-printable text = %q, want decoded 'café time'", text)
	}
	if !strings.Contains(html, "<b>hi</b>") {
		t.Errorf("base64 html = %q, want decoded '<b>hi</b>'", html)
	}
}

func TestParseEmlx_mixedWithAttachmentAndNestedAlternative(t *testing.T) {
	// multipart/mixed { multipart/alternative { text/plain, text/html }, image/png }.
	// The nested alternative must be descended; the image part must be skipped.
	rfc822 := "Content-Type: multipart/mixed; boundary=\"OUT\"\r\n\r\n" +
		"--OUT\r\nContent-Type: multipart/alternative; boundary=\"IN\"\r\n\r\n" +
		"--IN\r\nContent-Type: text/plain\r\n\r\nnested text\r\n" +
		"--IN\r\nContent-Type: text/html\r\n\r\n<i>nested html</i>\r\n" +
		"--IN--\r\n" +
		"--OUT\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\nAAAA\r\n" +
		"--OUT--\r\n"

	html, text, err := parseEmlx(wrapEmlx(rfc822))
	if err != nil {
		t.Fatalf("parseEmlx: %v", err)
	}
	if !strings.Contains(text, "nested text") {
		t.Errorf("text = %q, want nested text", text)
	}
	if !strings.Contains(html, "nested html") {
		t.Errorf("html = %q, want nested html", html)
	}
}

func TestParseEmlx_singlePartPlain(t *testing.T) {
	html, text, err := parseEmlx(wrapEmlx("Subject: S\r\nContent-Type: text/plain\r\n\r\nbody only"))
	if err != nil {
		t.Fatalf("parseEmlx: %v", err)
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
	if !strings.Contains(text, "body only") {
		t.Errorf("text = %q", text)
	}
}

func TestStripEmlxEnvelope_partialTruncation(t *testing.T) {
	// A .partial.emlx may declare a length longer than the bytes present; the slice
	// must clamp to what is available without panicking.
	data := []byte("9999\nshort body")
	got := stripEmlxEnvelope(data)
	if string(got) != "short body" {
		t.Errorf("stripEmlxEnvelope = %q, want 'short body'", got)
	}
}

func TestStripEmlxEnvelope_noLengthPrefix(t *testing.T) {
	// Without a valid leading length, the whole input is treated as RFC-822.
	data := []byte("Subject: x\r\n\r\nhello")
	got := stripEmlxEnvelope(data)
	if string(got) != string(data) {
		t.Errorf("stripEmlxEnvelope = %q, want the raw input", got)
	}
}

func TestParseEmlx_garbageDoesNotPanic(t *testing.T) {
	// Garbage input must not panic and must not fabricate body content (an error is
	// acceptable; reaching the assertions at all is the no-panic guard).
	html, text, _ := parseEmlx([]byte("\x00\x01\x02 not a message"))
	if html != "" || text != "" {
		t.Errorf("garbage produced html=%q text=%q, want empty", html, text)
	}
}
