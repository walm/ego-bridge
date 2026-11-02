// ego-bridge lets agents on a remote server drive the ego-browser running on
// your own computer, over an SSH reverse tunnel.
//
// On your computer:   ego-bridge ssh <host>     (or: ego-bridge serve)
// On the server:      ego-bridge install        (puts an `ego-browser` shim on PATH)
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

var version = "0.1.0"

const usageText = `ego-bridge: drive your local ego-browser from agents on remote servers.

On your computer (where ego lite runs):
  ego-bridge ssh [ssh args...] <host>   ssh to <host> with the bridge forwarded
  ego-bridge serve [--socket PATH]      standalone bridge for an ssh-config RemoteForward
  ego-bridge serve --listen local       direct TCP for OrbStack VMs / Docker containers
  ego-bridge serve --listen tailscale   direct TCP for machines on your tailnet
  ego-bridge token [--rotate]           print the token TCP clients must present

On the agent machine (server, VM, container):
  ego-bridge install [--addr HOST:PORT --token-stdin] [--force]
                                        install the ego-browser shim and agent skill;
                                        --addr saves a direct TCP bridge address
  ego-bridge status                     check the connection to your computer
  ego-browser ...                       (the shim) same CLI as the local ego-browser
  ego-bridge exec ...                   same as the shim, without the symlink

Environment (agent side):
  EGO_BRIDGE_SOCKET   use this unix socket
  EGO_BRIDGE_ADDR     use this TCP address (with EGO_BRIDGE_TOKEN)
  EGO_BRIDGE_TOKEN    token for TCP (default: from ~/.ego-bridge/remote.json)
  EGO_BRIDGE_ROOTS    colon-separated dirs whose paths map to your computer
                      (default: $HOME, /tmp, $TMPDIR, cwd)
`

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("[ego-bridge] ")
	if base := filepath.Base(os.Args[0]); base == "ego-browser" {
		os.Exit(runShim(os.Args[1:]))
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "ssh":
		os.Exit(cmdSSH(args))
	case "serve":
		os.Exit(cmdServe(args))
	case "exec", "run":
		os.Exit(runShim(args))
	case "install":
		os.Exit(cmdInstall(args))
	case "status":
		os.Exit(cmdStatus())
	case "token":
		os.Exit(cmdToken(args))
	case "version", "--version", "-v":
		fmt.Println("ego-bridge", version)
	case "help", "--help", "-h":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "ego-bridge: unknown command %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	defaultSock := filepath.Join(bridgeHome(), "bridge.sock")
	sock := fs.String("socket", defaultSock, "unix socket to listen on (for ssh RemoteForward)")
	var listens []string
	fs.Func("listen", "also listen on TCP, token required: local, tailscale, or host:port (default port "+defaultPort+"; repeatable)", func(v string) error {
		listens = append(listens, v)
		return nil
	})
	ego := fs.String("ego", "", "path to the real ego-browser (default: from PATH)")
	allowAll := fs.Bool("allow-all-commands", false, "also allow ego-browser upgrade/import over the bridge")
	fs.Parse(args)
	sockSet := false
	fs.Visit(func(f *flag.Flag) { sockSet = sockSet || f.Name == "socket" })

	egoPath, err := findEgoBrowser(*ego)
	if err != nil {
		log.Print(err)
		return 1
	}
	srv, err := newServer(egoPath, *allowAll)
	if err != nil {
		log.Print(err)
		return 1
	}

	var lns []net.Listener
	errc := make(chan error, 1+len(listens))
	// With --listen only, skip the unix socket unless it was asked for.
	if len(listens) == 0 || sockSet {
		ln, err := listenUnix(*sock)
		if err != nil {
			log.Print(err)
			return 1
		}
		defer os.Remove(*sock)
		lns = append(lns, ln)
		log.Printf("listening on unix socket %s", *sock)
		log.Printf("for ssh, add to ~/.ssh/config on this computer:\n\n  Host <your-server>\n    RemoteForward /tmp/ego-bridge-<server-user>.sock %s\n    ExitOnForwardFailure yes\n", *sock)
		go func() { errc <- srv.Serve(ln, false) }()
	}
	if len(listens) > 0 {
		if srv.Token, err = loadOrCreateToken(false); err != nil {
			log.Print("token: ", err)
			return 1
		}
		for _, l := range listens {
			addr, err := resolveListen(l)
			if err != nil {
				log.Print(err)
				return 1
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				log.Print(err)
				return 1
			}
			lns = append(lns, ln)
			log.Printf("listening on tcp %s, reachable from %s; token in %s", addr, exposure(addr), tokenPath())
			go func() { errc <- srv.Serve(ln, true) }()
		}
		log.Printf("set up an agent machine with:\n\n  ego-bridge token | <run on agent machine> ego-bridge install --addr <this-mac>:%s --token-stdin\n\n  (<this-mac> is host.orb.internal in OrbStack, host.docker.internal in Docker, or this Mac's tailnet name)\n", defaultPort)
	}
	log.Printf("serving %s", egoPath)

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		for _, ln := range lns {
			ln.Close()
		}
	}()
	for range lns {
		if err := <-errc; err != nil {
			log.Print(err)
			return 1
		}
	}
	return 0
}

func cmdToken(args []string) int {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	rotate := fs.Bool("rotate", false, "generate a new token (restart `ego-bridge serve` and reinstall agents)")
	fs.Parse(args)
	t, err := loadOrCreateToken(*rotate)
	if err != nil {
		log.Print(err)
		return 1
	}
	fmt.Println(t)
	return 0
}

// cmdSSH runs ssh with a private bridge reverse-forwarded to the server.
func cmdSSH(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ego-bridge ssh [ssh args...] <host> [command...]")
		return 2
	}
	egoPath, err := findEgoBrowser(os.Getenv("EGO_BRIDGE_EGO"))
	if err != nil {
		log.Print(err)
		return 1
	}
	srv, err := newServer(egoPath, false)
	if err != nil {
		log.Print(err)
		return 1
	}
	srv.Logf = func(string, ...any) {} // keep the interactive terminal clean

	id := randomID()
	localSock := filepath.Join(bridgeHome(), "run", "ssh-"+id+".sock")
	ln, err := listenUnix(localSock)
	if err != nil {
		log.Print(err)
		return 1
	}
	defer os.Remove(localSock)
	go srv.Serve(ln, false)

	remoteUser, err := sshRemoteUser(args)
	if err != nil {
		log.Printf("cannot resolve remote user with ssh -G: %v", err)
		return 1
	}
	remoteSock := fmt.Sprintf("/tmp/ego-bridge-%s-%s.sock", remoteUser, id)

	sshArgs := append([]string{
		"-o", "ExitOnForwardFailure=yes",
		"-R", remoteSock + ":" + localSock,
	}, args...)
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// ssh owns the terminal; let it handle Ctrl-C instead of dying first.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)
	go func() {
		for range sigs {
		}
	}()

	err = cmd.Run()
	ln.Close()
	return exitCode(err)
}

func sshRemoteUser(args []string) (string, error) {
	out, err := exec.Command("ssh", append([]string{"-G"}, args...)...).Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if u, ok := strings.CutPrefix(line, "user "); ok {
			u = strings.TrimSpace(u)
			for _, c := range u {
				if !(c == '-' || c == '_' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
					return "", fmt.Errorf("unsupported remote user name %q", u)
				}
			}
			return u, nil
		}
	}
	return "", fmt.Errorf("no user in ssh -G output")
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cmdStatus() int {
	tgt, err := findTarget()
	if err != nil {
		fmt.Println("not connected:", err)
		fmt.Println("from your computer run `ego-bridge ssh <this-host>`, or `ego-bridge serve --listen ...` and `ego-bridge install --addr ... --token-stdin` here")
		return exitNoBridge
	}
	fmt.Println("bridge:", tgt.desc)
	roots, capture := remoteRoots()
	fmt.Println("mapped dirs:", strings.Join(roots, ", "))
	if capture >= 0 {
		fmt.Println("captured temp files go to:", filepath.Join(roots[capture], "ego-bridge"))
	}
	fmt.Println("local ego-browser:")
	return runShim([]string{"--version"})
}
