package main

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// pathRef is one occurrence of a mapped root inside script text.
type pathRef struct {
	Root    int // index into roots
	Start   int // offset where the root path starts
	RootEnd int // offset just past the root path
	End     int // offset just past the whole path (>= RootEnd)
}

// Rel returns the path below the root ("" for the root itself).
func (r pathRef) Rel(text string) string {
	if r.End <= r.RootEnd+1 {
		return ""
	}
	return text[r.RootEnd+1 : r.End]
}

type rootMatcher struct {
	roots []string
	re    *regexp.Regexp
	index map[string]int
}

// normalizeRoots cleans, dedupes and drops roots nested inside other roots.
func normalizeRoots(in []string) []string {
	var cleaned []string
	seen := map[string]bool{}
	for _, r := range in {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		r = filepath.Clean(r)
		if r == "/" || seen[r] {
			continue
		}
		seen[r] = true
		cleaned = append(cleaned, r)
	}
	var out []string
	for _, r := range cleaned {
		nested := false
		for _, o := range cleaned {
			if o != r && strings.HasPrefix(r, o+"/") {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, r)
		}
	}
	return out
}

func newRootMatcher(roots []string) *rootMatcher {
	m := &rootMatcher{roots: roots, index: map[string]int{}}
	if len(roots) == 0 {
		return m
	}
	sorted := append([]string(nil), roots...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	alts := make([]string, len(sorted))
	for i, r := range sorted {
		alts[i] = regexp.QuoteMeta(r)
	}
	for i, r := range roots {
		m.index[r] = i
	}
	// A root only counts when it starts a path: at line start, after a quote,
	// whitespace, an opening bracket, '=', ',', ':' or a file:// scheme. This
	// keeps "/tmp" inside "https://host/tmp/x" untouched.
	m.re = regexp.MustCompile("(?m)(?:^|[\\s\"'`(\\[{=,:]|file://)(" + strings.Join(alts, "|") + ")")
	return m
}

func isPathNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.' || c == '~' || c == '+' || c == '@'
}

// isPathTerminator ends a path inside script text or output.
func isPathTerminator(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', ')', ']', '}', ',', ';', '<', '>', '|', '$', '\\':
		return true
	}
	return false
}

// find returns every root occurrence in text.
func (m *rootMatcher) find(text string) []pathRef {
	if m.re == nil {
		return nil
	}
	var refs []pathRef
	for _, loc := range m.re.FindAllStringSubmatchIndex(text, -1) {
		s, e := loc[2], loc[3]
		if e < len(text) {
			c := text[e]
			if c != '/' && isPathNameChar(c) {
				continue // e.g. "/tmpfoo" is not under "/tmp"
			}
		}
		end := e
		if e < len(text) && text[e] == '/' {
			end = e + 1
			for end < len(text) && !isPathTerminator(text[end]) {
				end++
			}
			// Trailing sentence punctuation is almost never part of a path.
			for end > e+1 && strings.ContainsRune(".:", rune(text[end-1])) {
				end--
			}
		}
		refs = append(refs, pathRef{Root: m.index[text[s:e]], Start: s, RootEnd: e, End: end})
	}
	return refs
}

// replaceRoots rewrites each root occurrence to the matching entry of to.
func (m *rootMatcher) replaceRoots(text string, to []string) string {
	refs := m.find(text)
	if len(refs) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, r := range refs {
		b.WriteString(text[last:r.Start])
		b.WriteString(to[r.Root])
		last = r.RootEnd
	}
	b.WriteString(text[last:])
	return b.String()
}

// safeRel reports whether p is a clean relative path that stays inside its root.
func safeRel(p string) bool {
	return p != "" && filepath.IsLocal(p)
}
