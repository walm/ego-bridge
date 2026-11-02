package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRoots(t *testing.T) {
	got := normalizeRoots([]string{"/home/u", "/tmp", "/home/u/proj", "rel", "/", "/tmp/", ""})
	want := []string{"/home/u", "/tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestReplaceRoots(t *testing.T) {
	m := newRootMatcher([]string{"/home/u", "/tmp"})
	to := []string{"/L/r0", "/L/r1"}
	cases := map[string]string{
		`page.screenshot({ path: "/home/u/a.png" })`: `page.screenshot({ path: "/L/r0/a.png" })`,
		`setInputFiles("x", ['/tmp/f.pdf', "/tmp"])`: `setInputFiles("x", ['/L/r1/f.pdf', "/L/r1"])`,
		"goto(`file:///home/u/site/index.html`)":     "goto(`file:///L/r0/site/index.html`)",
		`goto("https://example.com/tmp/x")`:          `goto("https://example.com/tmp/x")`,
		`"/tmpfoo/x" "/home/user2/x" "/home/u"`:      `"/tmpfoo/x" "/home/user2/x" "/L/r0"`,
		"/tmp/start-of-line\n  /home/u/indented":     "/L/r1/start-of-line\n  /L/r0/indented",
		`{"path":"/home/u/x"}`:                       `{"path":"/L/r0/x"}`,
		"`/home/u/${name}.png`":                      "`/L/r0/${name}.png`",
	}
	for in, want := range cases {
		if got := m.replaceRoots(in, to); got != want {
			t.Errorf("\n in: %s\ngot: %s\nwant: %s", in, got, want)
		}
	}
}

func TestFindRel(t *testing.T) {
	m := newRootMatcher([]string{"/home/u"})
	text := `a "/home/u/dir/file.pdf" saved to /home/u/out.png. and ` + "`/home/u/${x}`"
	var rels []string
	for _, r := range m.find(text) {
		rels = append(rels, r.Rel(text))
	}
	want := []string{"dir/file.pdf", "out.png", ""}
	if !reflect.DeepEqual(rels, want) {
		t.Fatalf("got %q want %q", rels, want)
	}
}

func TestInjectBridgeNote(t *testing.T) {
	guide := "# guide\nbody\n[ego-browser:skill] end of guide v2\n"
	out := injectBridgeNote(guide, []string{"/home/u", "/tmp"}, 1)
	if !strings.HasSuffix(out, "[ego-browser:skill] end of guide v2\n") {
		t.Fatalf("end marker must stay last:\n%s", out)
	}
	if !strings.Contains(out, "Remote session via ego-bridge") || !strings.Contains(out, "/tmp/ego-bridge") {
		t.Fatalf("note missing:\n%s", out)
	}
}

func TestCommandOf(t *testing.T) {
	if c := commandOf([]string{"--ego-server-name=a", "nodejs", "-e", "x"}); c != "nodejs" {
		t.Fatal(c)
	}
	if c := commandOf([]string{"--ego-server-name", "a", "upgrade"}); c != "upgrade" {
		t.Fatal(c)
	}
}
