package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultPort = "47777"

// ---- Mac side: token and listen addresses ----

func tokenPath() string { return filepath.Join(bridgeHome(), "token") }

// loadOrCreateToken returns the bridge token, creating a random one (0600)
// the first time. TCP clients must present it; unix-socket clients need not,
// because the socket's file permissions already restrict access.
func loadOrCreateToken(rotate bool) (string, error) {
	if !rotate {
		if b, err := os.ReadFile(tokenPath()); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return t, nil
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	t := hex.EncodeToString(b)
	if err := os.MkdirAll(bridgeHome(), 0o700); err != nil {
		return "", err
	}
	return t, os.WriteFile(tokenPath(), []byte(t+"\n"), 0o600)
}

// resolveListen expands "local", "tailscale", "local:PORT", "tailscale:PORT"
// and plain host:port into a concrete TCP address.
func resolveListen(v string) (string, error) {
	host, port := v, defaultPort
	if h, p, err := net.SplitHostPort(v); err == nil {
		host, port = h, p
	}
	switch host {
	case "local", "localhost":
		host = "127.0.0.1"
	case "tailscale":
		ip, err := tailscaleIP()
		if err != nil {
			return "", fmt.Errorf("tailscale address: %w", err)
		}
		host = ip
	case "":
		return "", fmt.Errorf("listen address %q has no host; use local, tailscale or an IP", v)
	}
	return net.JoinHostPort(host, port), nil
}

func tailscaleIP() (string, error) {
	bins := []string{"tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	var lastErr error
	for _, b := range bins {
		out, err := exec.Command(b, "ip", "-4").Output()
		if err != nil {
			lastErr = err
			continue
		}
		if ip := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]); ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("cannot run `tailscale ip -4`: %v", lastErr)
}

// exposure describes who can reach a listen address.
func exposure(addr string) string {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return "unknown network"
	}
	ip := ap.Addr()
	switch {
	case ip.IsLoopback():
		return "this Mac, its OrbStack/Docker VMs and containers (host.orb.internal / host.docker.internal)"
	case netip.MustParsePrefix("100.64.0.0/10").Contains(ip), netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip):
		return "your tailnet (limit further with Tailscale ACLs)"
	case ip.IsUnspecified():
		return "EVERY network this Mac is on"
	default:
		return "the network " + ip.String() + " is on"
	}
}

// ---- Server (agent) side: where to connect ----

type remoteConfig struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

func remoteConfigPath() string { return filepath.Join(bridgeHome(), "remote.json") }

func loadRemoteConfig() remoteConfig {
	var c remoteConfig
	if b, err := os.ReadFile(remoteConfigPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func saveRemoteConfig(c remoteConfig) error {
	if err := os.MkdirAll(bridgeHome(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(remoteConfigPath(), append(b, '\n'), 0o600)
}

type target struct {
	network, addr, token, desc string
}

func (t target) dial() (net.Conn, error) {
	return net.DialTimeout(t.network, t.addr, 5*time.Second)
}

// findTarget picks how to reach the Mac, in order:
//  1. $EGO_BRIDGE_SOCKET (unix socket)
//  2. $EGO_BRIDGE_ADDR   (TCP, token from $EGO_BRIDGE_TOKEN or remote.json)
//  3. a live socket forwarded by `ego-bridge ssh` / RemoteForward
//  4. the addr saved by `ego-bridge install --addr`
func findTarget() (target, error) {
	cfg := loadRemoteConfig()
	token := cfg.Token
	if t := os.Getenv("EGO_BRIDGE_TOKEN"); t != "" {
		token = t
	}
	if s := os.Getenv("EGO_BRIDGE_SOCKET"); s != "" {
		return target{"unix", s, token, "socket " + s + " ($EGO_BRIDGE_SOCKET)"}, nil
	}
	if a := os.Getenv("EGO_BRIDGE_ADDR"); a != "" {
		return target{"tcp", a, token, "tcp " + a + " ($EGO_BRIDGE_ADDR)"}, nil
	}
	if s, err := findSocket(); err == nil {
		return target{"unix", s, token, "ssh-forwarded socket " + s}, nil
	}
	if cfg.Addr != "" {
		return target{"tcp", cfg.Addr, token, "tcp " + cfg.Addr + " (" + remoteConfigPath() + ")"}, nil
	}
	return target{}, errors.New("no ssh-forwarded socket and no bridge address configured")
}

func readTokenStdin() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}
