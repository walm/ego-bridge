package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const exitNoBridge = 69 // EX_UNAVAILABLE

// remoteRoots returns the remote directories whose paths are mapped to the Mac:
// $EGO_BRIDGE_ROOTS (colon-separated) or $HOME, the temp dir and the cwd.
func remoteRoots() (roots []string, captureRoot int) {
	var in []string
	if env := os.Getenv("EGO_BRIDGE_ROOTS"); env != "" {
		in = strings.Split(env, ":")
	} else {
		if h, err := os.UserHomeDir(); err == nil {
			in = append(in, h)
		}
		in = append(in, "/tmp", os.TempDir())
		if wd, err := os.Getwd(); err == nil {
			in = append(in, wd)
		}
		// Also map symlink targets (/tmp -> /private/tmp on macOS) so
		// resolved paths printed by tools are mapped too.
		for _, r := range append([]string(nil), in...) {
			if real, err := filepath.EvalSymlinks(r); err == nil && real != r {
				in = append(in, real)
			}
		}
	}
	roots = normalizeRoots(in)
	captureRoot = -1
	tmp := filepath.Clean(os.TempDir())
	for i, r := range roots {
		if r == tmp || strings.HasPrefix(tmp, r+"/") {
			captureRoot = i
		}
	}
	if captureRoot < 0 && len(roots) > 0 {
		captureRoot = 0
	}
	return roots, captureRoot
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// findSocket locates a live bridge socket owned by this user. Sockets come
// from `ego-bridge ssh` (/tmp/ego-bridge-<user>-<id>.sock) or a fixed
// RemoteForward in ssh config (/tmp/ego-bridge-<user>.sock). Stale sockets
// left by dropped connections are removed.
func findSocket() (string, error) {
	name := currentUsername()
	cands, _ := filepath.Glob(fmt.Sprintf("/tmp/ego-bridge-%s-*.sock", name))
	cands = append(cands, fmt.Sprintf("/tmp/ego-bridge-%s.sock", name))
	type cand struct {
		path  string
		mtime time.Time
	}
	var live []cand
	uid := uint32(os.Getuid())
	for _, p := range cands {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			continue
		}
		// Only trust sockets we own: /tmp is shared with other users.
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != uid {
			continue
		}
		live = append(live, cand{p, info.ModTime()})
	}
	sort.Slice(live, func(i, j int) bool { return live[i].mtime.After(live[j].mtime) })
	for _, c := range live {
		conn, err := net.DialTimeout("unix", c.path, 2*time.Second)
		if err == nil {
			conn.Close()
			return c.path, nil
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			_ = os.Remove(c.path)
		}
	}
	return "", errors.New("no live bridge socket")
}

func needsStdin(args []string) bool {
	rest := args
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--ego-server-name") {
		if rest[0] == "--ego-server-name" && len(rest) > 1 {
			rest = rest[1:]
		}
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != "nodejs" {
		return false
	}
	for _, a := range rest[1:] {
		if a == "-e" || a == "--eval" || strings.HasPrefix(a, "--eval=") || a == "-h" || a == "--help" {
			return false
		}
	}
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return false
	}
	return true
}

// collectRefs finds remote files and directories referenced by the script.
func collectRefs(text string, roots []string) (files, dirs []FileData, warnings []string) {
	m := newRootMatcher(roots)
	seen := map[string]bool{}
	var total int64
	for _, ref := range m.find(text) {
		rel := ref.Rel(text)
		if rel == "" || !safeRel(rel) {
			continue
		}
		full := filepath.Join(roots[ref.Root], rel)
		if seen[full] {
			continue
		}
		seen[full] = true
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, FileData{Root: ref.Root, Path: rel})
		case info.Mode().IsRegular():
			if info.Size() > maxTransferFile || total+info.Size() > 4*maxTransferFile {
				warnings = append(warnings, fmt.Sprintf("[ego-bridge] not uploading %s: too large", full))
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("[ego-bridge] not uploading %s: %v", full, err))
				continue
			}
			total += info.Size()
			files = append(files, FileData{Root: ref.Root, Path: rel, Data: data, Mode: uint32(info.Mode().Perm())})
		}
	}
	return files, dirs, warnings
}

// runShim is `ego-browser <args>` on the remote server.
func runShim(args []string) int {
	notConnected := func(detail string) int {
		fmt.Fprintln(os.Stderr, `ego-bridge: not connected to the user's local ego-browser (`+detail+`).
This machine reaches the user's browser through ego-bridge running on their
computer, either over SSH (`+"`ego-bridge ssh <this-host>`"+`) or directly
(`+"`ego-bridge serve --listen local|tailscale`"+`). Stop and ask the user to start
or reconnect ego-bridge on their computer. Do not install ego-browser here.`)
		return exitNoBridge
	}
	tgt, err := findTarget()
	if err != nil {
		return notConnected(err.Error())
	}

	roots, captureRoot := remoteRoots()
	req := Request{Version: protocolVersion, Token: tgt.token, Args: args, Roots: roots, CaptureRoot: captureRoot}
	if needsStdin(args) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ego-bridge: read stdin:", err)
			return 1
		}
		req.Stdin, req.HasStdin = data, true
	}
	var warnings []string
	req.Files, req.Dirs, warnings = collectRefs(strings.Join(args, "\n")+"\n"+string(req.Stdin), roots)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}

	conn, err := tgt.dial()
	if err != nil {
		return notConnected(tgt.desc + ": " + err.Error())
	}
	defer conn.Close()

	var interrupted atomic.Int32
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s := <-sigs
		code := int32(130)
		if sig, ok := s.(syscall.Signal); ok {
			code = 128 + int32(sig)
		}
		interrupted.Store(code)
		conn.Close() // the Mac kills the run when the connection drops
	}()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintln(os.Stderr, "ego-bridge: send:", err)
		return exitNoBridge
	}

	isSkill := commandOf(args) == "skill"
	var skillOut strings.Builder
	dec := json.NewDecoder(conn)
	for {
		var f Frame
		if err := dec.Decode(&f); err != nil {
			if code := interrupted.Load(); code != 0 {
				return int(code)
			}
			fmt.Fprintln(os.Stderr, "ego-bridge: connection to the local browser lost:", err)
			return 255
		}
		switch f.Type {
		case "stdout":
			if isSkill {
				skillOut.Write(f.Data)
			} else {
				os.Stdout.Write(f.Data)
			}
		case "stderr":
			os.Stderr.Write(f.Data)
		case "error":
			fmt.Fprintln(os.Stderr, "ego-bridge:", f.Message)
		case "file":
			if err := writeBack(f.File, roots); err != nil {
				fmt.Fprintln(os.Stderr, "ego-bridge: write file:", err)
			}
		case "exit":
			if isSkill {
				fmt.Print(injectBridgeNote(skillOut.String(), roots, captureRoot))
			}
			return f.Code
		}
	}
}

func writeBack(f *FileData, roots []string) error {
	if f == nil || f.Root < 0 || f.Root >= len(roots) || !safeRel(f.Path) {
		return errors.New("invalid file frame")
	}
	dst := filepath.Join(roots[f.Root], f.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, f.Data, fileMode(f.Mode))
}

func injectBridgeNote(guide string, roots []string, captureRoot int) string {
	capture := "(disabled)"
	if captureRoot >= 0 {
		capture = filepath.Join(roots[captureRoot], "ego-bridge")
	}
	note := fmt.Sprintf(`
## Remote session via ego-bridge

This shell runs on a remote server. Here, `+"`ego-browser`"+` is the ego-bridge shim:
every command runs on the user's own computer, in their local ego lite browser,
and its output streams back here. Everything above still applies, with these
differences:

- The Node.js script runs on the user's computer, not on this server. Never use
  `+"`os.homedir()`, `os.tmpdir()`, `process.cwd()`"+` or relative paths to build file
  paths; they point at the user's computer.
- Absolute paths under these server directories are mapped: %s.
  Write each path as a string literal in the script, for example
  `+"`await page.screenshot({ path: \"%s/shot.png\" })`"+`.
  - Files the script writes under them (screenshots, downloads, `+"`saveAs`"+`,
    `+"`fs.writeFile`"+`) are copied back here to the same path after the run.
  - Input files (`+"`setInputFiles`"+`, `+"`chooser.setFiles`"+`, `+"`fs.readFile`"+`) must exist on
    this server and appear in the script as a full literal path; they are copied
    to the user's computer before the run. Paths built at runtime are not seen.
- A screenshot() without a path is copied back under %s and the
  printed path points there.
- If `+"`ego-browser`"+` reports that it is not connected, stop and ask the user to
  start or reconnect ego-bridge on their computer. Do not install ego lite on
  this machine.
- `+"`ego-browser upgrade`"+` and `+"`import`"+` are disabled here; ask the user to run them locally.

`, strings.Join(roots, ", "), roots[0], capture)
	const endMarker = "[ego-browser:skill] end of guide"
	if i := strings.LastIndex(guide, endMarker); i >= 0 {
		return guide[:i] + strings.TrimLeft(note, "\n") + "\n" + guide[i:]
	}
	return guide + note
}
