package fetcher

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extractPDFText pulls plain text out of PDF bytes using a pure-Go reader —
// no system dependency (poppler/pdftotext), keeping curio a single binary.
//
// ledongthuc/pdf is best-effort: it returns empty or garbled text on PDFs
// with awkward font encodings, and can even panic on malformed input. We
// recover panics into errors so the caller can fall back to Jina. The
// caller also enforces a minimum length to catch the "parsed but produced
// junk" case.
func extractPDFText(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("pdf: extractor panicked: %v", r)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf: open: %w", err)
	}
	body, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("pdf: extract: %w", err)
	}
	buf, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("pdf: read: %w", err)
	}
	return pdfTextToMarkdown(string(buf)), nil
}

var pdfBlankRunRE = regexp.MustCompile(`\n{3,}`)

// pdfTextToMarkdown lightly cleans extracted PDF text: trims, normalizes
// CRLF, and collapses long blank-line runs. PDF text has no real structure
// to recover, so this is plain text presented as (valid) markdown.
func pdfTextToMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = pdfBlankRunRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
