package gmail

import (
	"encoding/base64"
	"strings"

	gmailapi "google.golang.org/api/gmail/v1"
)

// selectBodies walks a message's MIME tree and returns its decoded HTML and
// plain-text bodies. HTML is always preferred for rendering and plain text is
// always captured as a fallback (and as the search corpus); the two are tracked
// independently, with the last non-empty part of each type winning. Only the two
// chosen parts are base64-decoded — earlier alternatives are skipped, not decoded
// and discarded. A nil part (or a part with no body data) contributes nothing
// rather than panicking, so a malformed payload degrades gracefully.
func selectBodies(part *gmailapi.MessagePart) (htmlBody, textBody string) {
	htmlData, textData := pickBodyData(part)
	return decodeB64URL(htmlData), decodeB64URL(textData)
}

// pickBodyData walks the MIME tree and returns the still-encoded data of the
// chosen text/html and text/plain parts (last non-empty of each type wins),
// recursing through multipart/* containers. Returning the raw data and deferring
// the decode to selectBodies means each body is decoded at most once.
func pickBodyData(part *gmailapi.MessagePart) (htmlData, textData string) {
	if part == nil {
		return "", ""
	}
	switch {
	case strings.HasPrefix(part.MimeType, "multipart/"):
		for _, sub := range part.Parts {
			h, t := pickBodyData(sub)
			if h != "" {
				htmlData = h
			}
			if t != "" {
				textData = t
			}
		}
	case part.MimeType == "text/html" && part.Body != nil:
		htmlData = part.Body.Data
	case part.MimeType == "text/plain" && part.Body != nil:
		textData = part.Body.Data
	}
	return htmlData, textData
}

// decodeB64URL decodes Gmail's base64url-encoded part data (RFC 4648 §5). Gmail
// omits padding, so the raw (unpadded) URL alphabet is used. Undecodable data
// yields the empty string rather than an error: a single malformed part should
// not fail the whole message body load.
func decodeB64URL(s string) string {
	if s == "" {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return ""
	}
	return string(b)
}
