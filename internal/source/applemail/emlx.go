package applemail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strconv"
	"strings"
)

// maxBodyBytes caps how much of any single text part is read, bounding memory on
// a pathological message. A couple of megabytes comfortably holds any real email
// body while refusing to buffer an unbounded stream.
const maxBodyBytes = 4 << 20 // 4 MiB

// parseEmlx decodes one Apple Mail .emlx (or .partial.emlx) file into its HTML and
// plain-text bodies. The .emlx container is a decimal byte-length line, then the
// RFC-822 message of exactly that length, then a trailing XML flags plist that we
// ignore. Either returned body may be empty (e.g. an HTML-only or text-only mail,
// or a .partial.emlx whose body was not downloaded); that is not an error.
func parseEmlx(data []byte) (htmlBody, textBody string, err error) {
	rfc822 := stripEmlxEnvelope(data)
	msg, err := mail.ReadMessage(bytes.NewReader(rfc822))
	if err != nil {
		return "", "", fmt.Errorf("parse emlx message: %w", err)
	}
	var b bodies
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	if err := walkPart(ct, msg.Header.Get("Content-Transfer-Encoding"), msg.Body, &b); err != nil {
		return "", "", fmt.Errorf("walk emlx body: %w", err)
	}
	return b.html, b.text, nil
}

// stripEmlxEnvelope returns the RFC-822 slice of an .emlx file. The first line is
// the byte length of the message; .partial.emlx files can declare a length longer
// than the bytes actually present, so the slice is clamped to what is available. A
// file without a valid leading length (an unexpected variant) is treated, as a
// best effort, as raw RFC-822.
func stripEmlxEnvelope(data []byte) []byte {
	if nl := bytes.IndexByte(data, '\n'); nl > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(string(data[:nl]))); err == nil && n > 0 {
			start := nl + 1
			end := min(start+n, len(data))
			if start <= end {
				return data[start:end]
			}
		}
	}
	return data
}

// bodies accumulates the first HTML and first plain-text part encountered while
// walking a (possibly multipart) message. First-wins matches how mail clients pick
// the primary alternative when several parts of the same type appear.
type bodies struct {
	html string
	text string
}

func (b *bodies) add(media string, content []byte) {
	switch media {
	case "text/html":
		if b.html == "" {
			b.html = string(content)
		}
	case "text/plain":
		if b.text == "" {
			b.text = string(content)
		}
	}
}

// walkPart recursively descends a MIME tree. For a multipart/* container it reads
// each child with the container boundary and recurses; for a text/* leaf it decodes
// the transfer encoding and records the content. Non-text leaves (attachments,
// images) are skipped. Charsets other than UTF-8 are not transcoded — a deliberate
// v1 simplification, since the overwhelming majority of mail is UTF-8 or ASCII.
func walkPart(contentType, cte string, r io.Reader, b *bodies) error {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || media == "" {
		media, params = "text/plain", nil
	}

	if strings.HasPrefix(media, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil
		}
		mr := multipart.NewReader(r, boundary)
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			pct := part.Header.Get("Content-Type")
			if pct == "" {
				pct = "text/plain"
			}
			perr := walkPart(pct, part.Header.Get("Content-Transfer-Encoding"), part, b)
			_ = part.Close()
			if perr != nil {
				return perr
			}
		}
	}

	if !strings.HasPrefix(media, "text/") {
		return nil // attachment / inline image — not body text
	}
	content, err := io.ReadAll(io.LimitReader(decodeTransfer(cte, r), maxBodyBytes))
	if err != nil {
		return err
	}
	b.add(media, content)
	return nil
}

// decodeTransfer wraps r to undo the part's Content-Transfer-Encoding. base64 and
// quoted-printable are the only encodings that need decoding; 7bit/8bit/binary and
// anything unrecognized pass through unchanged.
func decodeTransfer(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}
