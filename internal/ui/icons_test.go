package ui

import (
	"bytes"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iconBranchRE finds the names the icon template switches on.
var iconBranchRE = regexp.MustCompile(`\{\{-?\s*(?:else )?if eq \. "([a-z-]+)"\s*\}\}`)

// iconNames are the icons templates/icons.html defines.
func iconNames(t testing.TB) []string {
	t.Helper()
	src, err := fs.ReadFile(files, "templates/icons.html")
	require.NoError(t, err)
	branches := iconBranchRE.FindAllStringSubmatch(string(src), -1)
	names := make([]string, 0, len(branches))
	for _, m := range branches {
		names = append(names, m[1])
	}
	require.NotEmpty(t, names)
	return names
}

// iconUseRE finds a template's literal use of an icon.
var iconUseRE = regexp.MustCompile(`\{\{-?\s*template "icon" "([^"]*)"\s*-?\}\}`)

// usedIcons are the icons the pages can ask for: every literal use in a
// template, and every name a func that picks one can return.
func usedIcons(t testing.TB) []string {
	t.Helper()
	paths, err := fs.Glob(files, "templates/*.html")
	require.NoError(t, err)
	var used []string
	for _, path := range paths {
		src, err := fs.ReadFile(files, path)
		require.NoError(t, err)
		for _, m := range iconUseRE.FindAllStringSubmatch(string(src), -1) {
			used = append(used, m[1])
		}
	}
	for _, contentType := range append(slices.Clone(contentTypes), "", "bogus") {
		used = append(used, typeIcon(contentType))
	}
	for _, item := range navItems() {
		used = append(used, item.Icon)
	}
	slices.Sort(used)
	return slices.Compact(used)
}

// TestIcons: every icon a page asks for is defined, and every one defined
// is asked for; each is one inert, decorative <svg>, and a name that isn't
// one's renders nothing.
func TestIcons(t *testing.T) {
	defined := iconNames(t)
	assert.Len(t, defined, len(slices.Compact(slices.Sorted(slices.Values(defined)))), "each icon defined once")
	used := usedIcons(t)
	for _, name := range used {
		assert.Contains(t, defined, name, "an icon a page uses")
	}
	for _, name := range defined {
		assert.Contains(t, used, name, "an icon no page uses")
	}

	set := newRenderer(t).pages[PageStatus]
	svgRE := regexp.MustCompile(`^<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">(?:<[a-z]+ [^<>]*/>)+</svg>$`)
	for _, name := range defined {
		var buf bytes.Buffer
		require.NoError(t, set.ExecuteTemplate(&buf, "icon", name))
		assert.Regexp(t, svgRE, buf.String(), name)
	}
	var buf bytes.Buffer
	require.NoError(t, set.ExecuteTemplate(&buf, "icon", "no-such-icon"))
	assert.Empty(t, buf.String())
}

// TestIcons_LoadNothing: the icons reference nothing, a sprite's <use>
// fragment included, and carry no style; their file carries Lucide's
// license notice.
func TestIcons_LoadNothing(t *testing.T) {
	src, err := fs.ReadFile(files, "templates/icons.html")
	require.NoError(t, err)
	markup := strings.ToLower(templateCommentRE.ReplaceAllString(string(src), ""))
	for _, banned := range []string{"<use", "href", "xlink:", "style", "url("} {
		assert.NotContains(t, markup, banned)
	}
	assert.Contains(t, string(src), "ISC License")
	assert.Contains(t, string(src), "Cole Bemis 2013-2022 as\npart of Feather (MIT)")
	assert.Contains(t, string(src), "Permission to use, copy, modify, and/or distribute this software")
}
