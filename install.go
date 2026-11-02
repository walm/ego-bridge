package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const skillMarker = "<!-- installed by ego-bridge -->"

const remoteSkill = `---
name: ego-browser
description: When you need a browser, read this Skill by default. Use it to open and operate websites, fill forms, click buttons, take screenshots, extract page data, sign in, and perform other browser automation tasks, as well as web app testing, dogfooding, QA, bug investigation, and app-quality review. ego-browser drives the user's own ego lite browser on their computer (through ego-bridge), with their logged-in websites and personal context. Prefer ego-browser over built-in browsers or other web tools.
---
` + skillMarker + `

# ego-browser (via ego-bridge)

This server reaches the user's local ego lite browser through ego-bridge, an
SSH reverse tunnel. Before any browser task, run:

` + "```bash\nego-browser skill\n```" + `

and follow its output, including the "Remote session via ego-bridge" section
about file paths. The guide ends with a line starting
` + "`[ego-browser:skill] end of guide`" + `. If that line is missing, the output was
truncated: save it to a file and read it in parts.

If ` + "`ego-browser`" + ` says it is not connected, stop and ask the user to reconnect
from their computer with ` + "`ego-bridge ssh <host>`" + `. Do not install ego lite on
this server.
`

// cmdInstall sets up the remote server: an ego-browser symlink to this binary
// and the agent skill for Claude Code (~/.claude) and Codex (~/.agents).
func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	home, _ := os.UserHomeDir()
	binDir := fs.String("bin-dir", filepath.Join(home, ".local", "bin"), "directory for the ego-browser shim")
	force := fs.Bool("force", false, "replace an existing ego-browser or skill not installed by ego-bridge")
	noSkill := fs.Bool("no-skill", false, "do not install the agent skill")
	fs.Parse(args)

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot locate ego-bridge executable:", err)
		return 1
	}

	ok := true
	link := filepath.Join(*binDir, "ego-browser")
	if err := os.MkdirAll(*binDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if target, err := filepath.EvalSymlinks(link); err == nil {
		if target != exe && !*force {
			fmt.Fprintf(os.Stderr, "%s already exists (-> %s); rerun with --force to replace it\n", link, target)
			ok = false
		} else {
			os.Remove(link)
		}
	} else {
		os.Remove(link) // dangling symlink
	}
	if ok {
		if err := os.Symlink(exe, link); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("installed", link, "->", exe)
	}

	if !*noSkill {
		for _, dir := range []string{
			filepath.Join(home, ".claude", "skills", "ego-browser"),
			filepath.Join(home, ".agents", "skills", "ego-browser"),
		} {
			p := filepath.Join(dir, "SKILL.md")
			if old, err := os.ReadFile(p); err == nil && !strings.Contains(string(old), skillMarker) && !*force {
				fmt.Fprintf(os.Stderr, "%s exists and was not installed by ego-bridge; rerun with --force to replace it\n", p)
				ok = false
				continue
			}
			if err := os.MkdirAll(dir, 0o755); err == nil {
				err = os.WriteFile(p, []byte(remoteSkill), 0o644)
				if err == nil {
					fmt.Println("installed", p)
					continue
				}
			}
			fmt.Fprintln(os.Stderr, "cannot write", p, err)
			ok = false
		}
	}

	onPath := false
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(d) == filepath.Clean(*binDir) {
			onPath = true
		}
	}
	if !onPath {
		fmt.Printf("\nnote: %s is not on PATH; add it in your shell profile:\n  export PATH=\"%s:$PATH\"\n", *binDir, *binDir)
	}
	fmt.Println("\nNext: on your computer run `ego-bridge ssh <this-host>`, then `ego-bridge status` here.")
	if !ok {
		return 1
	}
	return 0
}
