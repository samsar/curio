package importer

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleHTML = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<!-- This is an automatically generated file. -->
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><A HREF="https://martinfowler.com/articles/feature-toggles.html" ADD_DATE="1727140000" TAGS="dx,flags">Feature Toggles</A>
    <DT><H3 ADD_DATE="1727140100">Tech</H3>
    <DL><p>
        <DT><A HREF="https://example.com/postgres-mvcc" ADD_DATE="1727140200">Postgres MVCC</A>
        <DT><H3>AI</H3>
        <DL><p>
            <DT><A HREF="https://example.com/agents">Agents</A>
        </DL><p>
    </DL><p>
    <DT><A HREF="javascript:alert(1)">Bookmarklet</A>
    <DT><A HREF="chrome://bookmarks/">Chrome bookmarks page</A>
    <DT><A HREF="https://example.com/no-date">No date here</A>
</DL><p>`

func TestParseHTML_Basic(t *testing.T) {
	got, err := ParseHTML(strings.NewReader(sampleHTML))
	require.NoError(t, err)

	// We get every <A>, including the chrome:// and javascript:; filtering
	// is the Indexable function's job, not the parser's.
	require.Len(t, got, 6)

	first := got[0]
	assert.Equal(t, "Feature Toggles", first.Title)
	assert.Equal(t, "https://martinfowler.com/articles/feature-toggles.html", first.URL)
	assert.Empty(t, first.FolderPath, "top-level bookmark has empty folder")
	assert.Equal(t, []string{"dx", "flags"}, first.Tags)
	assert.Equal(t, time.Unix(1727140000, 0).UTC(), first.SavedAt)

	// Nested folders.
	var mvcc, agents *ParsedBookmark
	for i := range got {
		switch got[i].URL {
		case "https://example.com/postgres-mvcc":
			mvcc = &got[i]
		case "https://example.com/agents":
			agents = &got[i]
		}
	}
	require.NotNil(t, mvcc)
	require.NotNil(t, agents)
	assert.Equal(t, "/Tech", mvcc.FolderPath)
	assert.Equal(t, "/Tech/AI", agents.FolderPath)
}

func TestIndexable(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		want    bool
		wantWhy FilterReason
	}{
		{"normal https", "https://example.com/x", true, ""},
		{"normal http", "http://example.com/x", true, ""},
		{"empty", "", false, ReasonEmpty},
		{"whitespace", "   ", false, ReasonEmpty},
		{"javascript", "javascript:alert(1)", false, ReasonJavaScript},
		{"file://", "file:///Users/me/x.pdf", false, ReasonLocalFile},
		{"chrome://", "chrome://bookmarks/", false, ReasonBrowserInternal},
		{"about:", "about:blank", false, ReasonBrowserInternal},
		{"edge://", "edge://settings", false, ReasonBrowserInternal},
		{"chrome-extension", "chrome-extension://abc/page.html", false, ReasonBrowserInternal},
		{"unknown scheme", "ftp://example.com", false, ReasonUnsupportedSchem},
		{"case insensitive", "JAVASCRIPT:x", false, ReasonJavaScript},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := Indexable(tc.url)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantWhy, why)
		})
	}
}

func TestParseHTML_Empty(t *testing.T) {
	in := `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p></DL><p>`
	_, err := ParseHTML(strings.NewReader(in))
	assert.ErrorIs(t, err, ErrEmpty)
}

func TestParseHTML_DeepNesting(t *testing.T) {
	in := `<DL><p>
		<DT><H3>A</H3>
		<DL><p>
			<DT><H3>B</H3>
			<DL><p>
				<DT><H3>C</H3>
				<DL><p>
					<DT><A HREF="https://example.com/x">Deep</A>
				</DL><p>
			</DL><p>
		</DL><p>
	</DL><p>`
	got, err := ParseHTML(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/A/B/C", got[0].FolderPath)
}

func TestParseHTML_FolderNameWithSlash(t *testing.T) {
	in := `<DL><p>
		<DT><H3>Tech/AI</H3>
		<DL><p><DT><A HREF="https://example.com">x</A></DL><p>
	</DL><p>`
	got, err := ParseHTML(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, got, 1)
	// Slashes in folder names are replaced with hyphens.
	assert.Equal(t, "/Tech-AI", got[0].FolderPath)
}

func TestParseHTML_MicrosecondEpoch(t *testing.T) {
	// Firefox-style ADD_DATE in microseconds since epoch.
	in := `<DL><p><DT><A HREF="https://example.com" ADD_DATE="1727140000000000">x</A></DL><p>`
	got, err := ParseHTML(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, time.Unix(1727140000, 0).UTC(), got[0].SavedAt)
}
