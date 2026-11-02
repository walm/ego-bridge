package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxTransferFile = 256 << 20 // 256 MiB per file

// allowedCommands are the ego-browser subcommands an agent needs. `upgrade`
// and `import` change the local install or profiles and stay local-only
// unless the server runs with --allow-all-commands.
var allowedCommands = map[string]bool{
	"": true, "nodejs": true, "skill": true, "help": true,
	"--help": true, "-h": true, "--version": true, "-v": true,
}

type Server struct {
	EgoPath     string
	StagingBase string
	AllowAll    bool
	Token       string // required on TCP listeners
	Logf        func(format string, args ...any)

	tempRe *regexp.Regexp // local temp paths printed by a run
}

func newServer(egoPath string, allowAll bool) (*Server, error) {
	base := filepath.Join(bridgeHome(), "staging")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	s := &Server{EgoPath: egoPath, StagingBase: base, AllowAll: allowAll, Logf: log.Printf}
	prefixes := map[string]bool{"/tmp": true, "/private/tmp": true}
	if t := strings.TrimSuffix(filepath.Clean(os.TempDir()), "/"); t != "" {
		prefixes[t] = true
		if r, err := filepath.EvalSymlinks(t); err == nil {
			prefixes[r] = true
		}
	}
	var alts []string
	for p := range prefixes {
		alts = append(alts, regexp.QuoteMeta(p))
	}
	s.tempRe = regexp.MustCompile("(?:" + strings.Join(alts, "|") + ")/[^\\s\"'`,;)\\]}<>|]+")
	return s, nil
}

// Serve accepts bridge connections. With requireToken (TCP listeners) every
// request must carry s.Token; unix sockets rely on file permissions instead.
func (s *Server) Serve(ln net.Listener, requireToken bool) error {
	if requireToken && s.Token == "" {
		return errors.New("refusing to serve TCP without a token")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn, requireToken)
	}
}

type frameWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
	err error
}

func (w *frameWriter) send(f Frame) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = w.enc.Encode(f)
	}
}

// commandOf returns the subcommand after global --ego-server-name options.
func commandOf(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--ego-server-name=") {
			continue
		}
		if a == "--ego-server-name" {
			i++
			continue
		}
		return a
	}
	return ""
}

func (s *Server) handle(conn net.Conn, requireToken bool) {
	defer conn.Close()
	// Bound how long and how much a peer may send before it is authenticated.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	dec := json.NewDecoder(io.LimitReader(conn, 5*maxTransferFile))
	var req Request
	if err := dec.Decode(&req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	w := &frameWriter{enc: json.NewEncoder(conn)}
	failCode := func(code int, msg string) {
		w.send(Frame{Type: "error", Message: msg})
		w.send(Frame{Type: "exit", Code: code})
	}
	fail := func(msg string) { failCode(2, msg) }
	if requireToken && subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.Token)) != 1 {
		s.Logf("rejected %s: bad token", conn.RemoteAddr())
		failCode(77, "invalid bridge token; on the Mac run `ego-bridge token` and update EGO_BRIDGE_TOKEN or rerun `ego-bridge install --addr ... --token-stdin` here")
		return
	}
	if req.Version != protocolVersion {
		fail(fmt.Sprintf("protocol version mismatch: client %d, server %d; install the same ego-bridge version on both ends", req.Version, protocolVersion))
		return
	}
	cmdName := commandOf(req.Args)
	if !s.AllowAll && !allowedCommands[cmdName] {
		fail(fmt.Sprintf("ego-browser %q is disabled over the bridge; run it on the local computer", cmdName))
		return
	}
	roots := normalizeRoots(req.Roots)
	if len(roots) != len(req.Roots) {
		fail("invalid roots: must be absolute, distinct, non-nested and not /")
		return
	}

	staging, err := os.MkdirTemp(s.StagingBase, "req-")
	if err != nil {
		fail("staging: " + err.Error())
		return
	}
	defer os.RemoveAll(staging)

	locals := make([]string, len(roots))
	for i := range roots {
		locals[i] = filepath.Join(staging, fmt.Sprintf("r%d", i))
		if err := os.MkdirAll(locals[i], 0o700); err != nil {
			fail("staging: " + err.Error())
			return
		}
	}
	toLocal := newRootMatcher(roots)

	// Inputs.
	uploaded := map[string][32]byte{}
	for _, d := range req.Dirs {
		if d.Root < 0 || d.Root >= len(locals) || (d.Path != "" && !safeRel(d.Path)) {
			fail("invalid dir path " + d.Path)
			return
		}
		_ = os.MkdirAll(filepath.Join(locals[d.Root], d.Path), 0o700)
	}
	for _, f := range req.Files {
		if f.Root < 0 || f.Root >= len(locals) || !safeRel(f.Path) {
			fail("invalid file path " + f.Path)
			return
		}
		dst := filepath.Join(locals[f.Root], f.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			fail(err.Error())
			return
		}
		if err := os.WriteFile(dst, f.Data, fileMode(f.Mode)); err != nil {
			fail(err.Error())
			return
		}
		uploaded[dst] = sha256.Sum256(f.Data)
	}

	// Map remote paths to staging in argv and stdin, and pre-create the
	// parent directory of every referenced path so writes succeed.
	scriptText := strings.Join(req.Args, "\n") + "\n" + string(req.Stdin)
	for _, ref := range toLocal.find(scriptText) {
		rel := ref.Rel(scriptText)
		if rel != "" && safeRel(rel) {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(locals[ref.Root], rel)), 0o700)
		}
	}
	args := make([]string, len(req.Args))
	for i, a := range req.Args {
		args[i] = toLocal.replaceRoots(a, locals)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Client hang-up (Ctrl-C, ssh drop) cancels the run.
	go func() {
		_, _ = io.Copy(io.Discard, io.MultiReader(dec.Buffered(), conn))
		cancel()
	}()

	cmd := exec.CommandContext(ctx, s.EgoPath, args...)
	cmd.Dir = staging
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if req.HasStdin {
		cmd.Stdin = strings.NewReader(toLocal.replaceRoots(string(req.Stdin), locals))
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	// Output mapping: staging paths back to remote roots.
	var pairs []string
	for i := range roots {
		pairs = append(pairs, locals[i], roots[i])
	}
	toRemote := strings.NewReplacer(pairs...)
	capt := &capturer{s: s, start: time.Now().Add(-2 * time.Second)}
	if req.CaptureRoot >= 0 && req.CaptureRoot < len(locals) {
		capt.dir = filepath.Join(locals[req.CaptureRoot], "ego-bridge")
	}

	s.Logf("run %q (%d input files)", cmdName, len(req.Files))
	if err := cmd.Start(); err != nil {
		fail("start ego-browser: " + err.Error())
		return
	}
	var wg sync.WaitGroup
	pump := func(r io.Reader, typ string) {
		defer wg.Done()
		br := bufio.NewReaderSize(r, 64<<10)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				line = toRemote.Replace(capt.rewrite(line))
				w.send(Frame{Type: typ, Data: []byte(line)})
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	wg.Wait()
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		s.Logf("run %q cancelled by client", cmdName)
		return
	}

	// Outputs: everything new or changed under staging goes back.
	for i, local := range locals {
		_ = filepath.WalkDir(local, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(local, p)
			if info.Size() > maxTransferFile {
				w.send(Frame{Type: "stderr", Data: []byte(fmt.Sprintf("[ego-bridge] skipped %s: larger than %d MiB\n", filepath.Join(roots[i], rel), maxTransferFile>>20))})
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if h, ok := uploaded[p]; ok && h == sha256.Sum256(data) {
				return nil
			}
			w.send(Frame{Type: "file", File: &FileData{Root: i, Path: rel, Data: data, Mode: uint32(info.Mode().Perm())}})
			return nil
		})
	}

	w.send(Frame{Type: "exit", Code: exitCode(waitErr)})
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

func fileMode(m uint32) os.FileMode {
	if m == 0 {
		return 0o644
	}
	return os.FileMode(m).Perm() | 0o600
}

// capturer copies local temp files a run prints (screenshot() without a path,
// for example) into the capture root so they reach the remote side.
type capturer struct {
	s     *Server
	dir   string
	start time.Time
	mu    sync.Mutex
	done  map[string]string // local temp path -> staging copy
}

func (c *capturer) rewrite(line string) string {
	if c.dir == "" {
		return line
	}
	return c.s.tempRe.ReplaceAllStringFunc(line, func(p string) string {
		trimmed := strings.TrimRight(p, ".:")
		if dst := c.capture(trimmed); dst != "" {
			return dst + p[len(trimmed):]
		}
		return p
	})
}

func (c *capturer) capture(p string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dst, ok := c.done[p]; ok {
		return dst
	}
	if strings.HasPrefix(p, c.s.StagingBase) {
		return ""
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(c.start) || info.Size() > maxTransferFile {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return ""
	}
	base := filepath.Base(p)
	dst := filepath.Join(c.dir, base)
	for n := 1; ; n++ {
		existing, err := os.ReadFile(dst)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && bytes.Equal(existing, data)) {
			break
		}
		ext := filepath.Ext(base)
		dst = filepath.Join(c.dir, fmt.Sprintf("%s-%d%s", strings.TrimSuffix(base, ext), n, ext))
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()|0o600); err != nil {
		return ""
	}
	if c.done == nil {
		c.done = map[string]string{}
	}
	c.done[p] = dst
	return dst
}

// listenUnix listens on a private unix socket, replacing a stale one.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s is already in use by a running bridge", path)
	}
	_ = os.Remove(path)
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	return ln, err
}

func bridgeHome() string {
	if h := os.Getenv("EGO_BRIDGE_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ego-bridge")
}

func findEgoBrowser(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	p, err := exec.LookPath("ego-browser")
	if err != nil {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, ".local", "bin", "ego-browser")
		if _, statErr := os.Stat(p); statErr != nil {
			return "", errors.New("ego-browser not found on PATH; pass --ego /path/to/ego-browser")
		}
	}
	// Never point the server at our own shim.
	if real, err := filepath.EvalSymlinks(p); err == nil {
		if self, err := os.Executable(); err == nil {
			if selfReal, err := filepath.EvalSymlinks(self); err == nil && selfReal == real {
				return "", errors.New("ego-browser on PATH is the ego-bridge shim; pass --ego with the real ego-browser")
			}
		}
	}
	return p, nil
}
