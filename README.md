# ego-bridge

Let coding agents on other machines drive the **[ego lite](https://lite.ego.app/)
browser running on your Mac**, with your logged-in sessions, your profile and
your screen. Those machines can be SSH servers, OrbStack VMs, Docker containers
or Tailscale hosts.

On the agent machine, `ego-bridge` installs a stand-in `ego-browser` command.
Agents use it exactly like the real CLI, and every call runs on your Mac.

```
 agent machine (server, VM, container)            your Mac
┌──────────────────────────┐  SSH tunnel, or    ┌────────────────────────────────┐
│ agent                    │  TCP + token       │ ego-bridge ssh / serve         │
│  └─ ego-browser nodejs … │ ─────────────────▶ │  └─ real ego-browser nodejs …  │
│     (ego-bridge shim)    │ ◀───────────────── │      └─ ego lite browser       │
│  files under $HOME, /tmp │  stdout/stderr,    │                                │
│                          │  exit code, files  │                                │
└──────────────────────────┘                    └────────────────────────────────┘
```

## Requirements

- **ego lite on your Mac.** Install it from **https://lite.ego.app/** and check
  that `ego-browser --version` works in a terminal.
- macOS (arm64/amd64) on the browser side; Linux or macOS (amd64/arm64) on the agent side.

## Install

On your Mac **and** on each agent machine:

```bash
curl -fsSL https://raw.githubusercontent.com/walm/ego-bridge/main/install.sh | sh
```

This installs `~/.local/bin/ego-bridge` from the
[latest release](https://github.com/walm/ego-bridge/releases/latest), after
verifying its SHA-256 checksum. Set `EGO_BRIDGE_VERSION=v0.1.0` to pin a version and
`EGO_BRIDGE_BIN_DIR=/usr/local/bin` to install elsewhere. Release binaries can
also be downloaded directly from the releases page.

## Let an agent set it up

Paste this into Claude Code or Codex **on your Mac**:

```text
Set up ego-bridge (https://github.com/walm/ego-bridge) so agents on my other
machines can drive the ego lite browser on this Mac. Read the README at that
URL first.

1. Run `ego-browser --version`. If it fails, stop and tell me to install ego
   lite from https://lite.ego.app/ first.
2. Install ego-bridge on this Mac:
   curl -fsSL https://raw.githubusercontent.com/walm/ego-bridge/main/install.sh | sh
3. Ask me which agent machines to connect, and how each is reached: SSH host
   (I connect interactively), Tailscale host, OrbStack machine, or Docker
   container.
4. For each machine, install ego-bridge there with the same curl command,
   then configure it:
   - SSH host: `ssh HOST '~/.local/bin/ego-bridge install'`, then tell me to
     connect with `ego-bridge ssh HOST` from now on.
   - OrbStack machine NAME: run the commands with `orb -m NAME sh -c '...'`;
     configure with
     `ego-bridge token | orb -m NAME sh -c '~/.local/bin/ego-bridge install --addr host.orb.internal:47777 --token-stdin'`
   - Tailscale host: `ego-bridge token | ssh HOST '~/.local/bin/ego-bridge install --addr <this Mac's tailnet name>:47777 --token-stdin'`
   - Docker: show me the Dockerfile and `docker run` lines from the README.
5. If any machine uses the direct modes (OrbStack, Docker, Tailscale), start
   the bridge on this Mac in the background:
   `mkdir -p ~/.ego-bridge && nohup ego-bridge serve --listen local > ~/.ego-bridge/serve.log 2>&1 &`
   (add `--listen tailscale` for Tailscale hosts).
6. Make sure ~/.local/bin is on PATH on every machine (add it to the shell
   profile if missing), run `ego-bridge status` on each one, and report the
   results to me.

Rules: never print, echo or paste the bridge token into a command line or
this chat. Always pipe `ego-bridge token` into `--token-stdin`. Do not
install ego lite on the agent machines.
```

## Manual setup

Pick the mode that matches how you reach the agent machine.

### SSH servers

```bash
# on the server, once
ego-bridge install
# from your Mac, instead of `ssh myserver`
ego-bridge ssh myserver
# on the server
ego-bridge status
```

`ego-bridge install` links `~/.local/bin/ego-browser` → `ego-bridge`. It also
writes an ego-browser skill to `~/.claude/skills/ego-browser` (Claude Code) and
`~/.agents/skills/ego-browser` (Codex).

`ego-bridge ssh` takes any ssh arguments. It starts a private bridge for that
session and forwards it with `-R /tmp/ego-bridge-<user>-<id>.sock:<local socket>`.
The shim on the server picks the newest live socket owned by your user, so
several sessions, tmux and reconnects work. Stale sockets are removed.

### OrbStack VMs, Docker containers, Tailscale (direct mode)

Unix sockets can't cross OrbStack's VM boundary, and OrbStack's built-in SSH
rejects socket forwards. For these, and for headless agents on your tailnet,
run the bridge on TCP with a token:

```bash
# On the Mac; keep it running
ego-bridge serve --listen local                      # 127.0.0.1:47777, for VMs/containers on this Mac
ego-bridge serve --listen tailscale                  # this Mac's tailnet IP, for other machines
ego-bridge serve --listen local --listen tailscale   # both
ego-bridge token                                     # created in ~/.ego-bridge/token (0600)
```

**OrbStack machine** (the token goes over stdin, never onto a command line):

```bash
orb -m my-vm sh -c 'curl -fsSL https://raw.githubusercontent.com/walm/ego-bridge/main/install.sh | sh'
ego-bridge token | orb -m my-vm sh -c '~/.local/bin/ego-bridge install --addr host.orb.internal:47777 --token-stdin'
```

**Tailscale host:**

```bash
ssh my-box 'curl -fsSL https://raw.githubusercontent.com/walm/ego-bridge/main/install.sh | sh'
ego-bridge token | ssh my-box '~/.local/bin/ego-bridge install --addr my-mac:47777 --token-stdin'
```

**Docker container:**

```dockerfile
RUN curl -fsSL https://raw.githubusercontent.com/walm/ego-bridge/main/install.sh \
      | EGO_BRIDGE_BIN_DIR=/usr/local/bin sh \
 && ego-bridge install --bin-dir /usr/local/bin
```

```bash
docker run -e EGO_BRIDGE_ADDR=host.docker.internal:47777 \
           -e EGO_BRIDGE_TOKEN="$(ego-bridge token)" …
```

### Long-lived SSH forward (ssh config, VS Code Remote)

```bash
ego-bridge serve        # on the Mac
```

```sshconfig
# ~/.ssh/config on the Mac
Host myserver
  RemoteForward /tmp/ego-bridge-<server-user>.sock /Users/<you>/.ego-bridge/bridge.sock
```

If reconnecting fails with "remote port forwarding failed", a stale socket is
still there from the last session. Set `StreamLocalBindUnlink yes` in the
server's `sshd_config`, or use `ego-bridge ssh`, which uses unique socket names.

## How it works, and why every ego-browser feature works

Agents use ego-browser through a CLI: `ego-browser nodejs` runs a JavaScript
script against the browser, and `ego-browser skill` / `help` print docs. Each
call is a new process. Task spaces, tabs and page labels persist in the
browser, not the process. So the shim forwards **argv + stdin**, and streams
back **stdout, stderr and the exit code**. Task spaces, pages, snapshots,
`evaluate`, `cdp`, `fetch`, popups, dialogs, `handOff`/`takeOverTaskSpace` and
`--ego-server-name` all work unchanged. Ctrl-C on the agent machine kills the
run on the Mac.

The one thing that doesn't carry over by itself is **file paths**, because
the script runs on your Mac. ego-bridge maps them:

| Script does (on the agent machine) | What happens |
| --- | --- |
| `page.screenshot({ path: "/home/me/shots/a.png" })` | Rewritten to a per-run staging dir on the Mac; the PNG is copied back to `/home/me/shots/a.png` |
| `page.screenshot()` (no path) | The Mac temp file is copied back to `/tmp/ego-bridge/…` and the printed path rewritten |
| `page.setInputFiles("#f", ["/home/me/report.pdf"])` | The file is uploaded to staging before the run |
| `download.saveAs("/tmp/x.csv")`, `page.fetch(u, { saveAs })`, `fs.writeFile` | Copied back after the run |
| `page.goto("file:///home/me/site/index.html")` | The file is uploaded and the URL rewritten |

Mapped dirs default to the agent machine's `$HOME`, `/tmp`, `$TMPDIR` and the
cwd (override with `EGO_BRIDGE_ROOTS=/a:/b`). Input files must appear
**literally** in the script to be uploaded. Outputs are found by sweeping
staging, so paths built at runtime under a literal mapped dir work too.
`ego-browser skill` on the agent machine adds a section explaining this to the
agent.

### Configuration (agent side)

| Variable | Meaning |
| --- | --- |
| `EGO_BRIDGE_SOCKET` | Use this unix socket |
| `EGO_BRIDGE_ADDR` | Use this TCP address (with `EGO_BRIDGE_TOKEN`) |
| `EGO_BRIDGE_TOKEN` | Token for TCP (default: `~/.ego-bridge/remote.json`) |
| `EGO_BRIDGE_ROOTS` | Colon-separated dirs mapped to the Mac |

Order of resolution: `EGO_BRIDGE_SOCKET`, `EGO_BRIDGE_ADDR`, a live SSH-forwarded
socket, then the address saved by `ego-bridge install --addr`.

## Security model: read this

A bridged agent can run **arbitrary JavaScript in Node on your Mac**. ego-browser's
runtime has full `node:fs`, `child_process` and network access, plus your
logged-in browser. That is the same power the agent would have locally, but it
now comes from another machine. So:

- Only bridge to machines you trust as much as your laptop. Root on the agent
  machine can use the bridge while it is connected.
- **SSH mode:** nothing listens on TCP. The forwarded socket on the server is
  `0600` (sshd's default `StreamLocalBindMask`), and the shim only uses sockets
  owned by its own uid. The bridge exists only while your SSH session is up.
- **Direct mode:** every TCP request must carry the token, which is compared in
  constant time. A bad token gets exit code 77 and is logged on the Mac. Every
  container and VM on your Mac can reach `127.0.0.1:47777`, and your whole
  tailnet can reach the tailnet port. The token is what keeps them out, so treat
  it like an SSH key. Rotate it with `ego-bridge token --rotate`, then restart
  `serve` and reinstall the agent machines. On Tailscale, also restrict port
  47777 with ACLs.
- `ego-browser upgrade` and `import` are refused over the bridge
  (`serve --allow-all-commands` to allow).

## Limits

- Script output reaches the agent as ego-browser emits it, which may be only
  when the script ends.
- Files over 256 MiB are not transferred.
- Unix socket paths are limited to about 104 characters on macOS.

## Development

```bash
make build     # bin/ego-bridge
make test      # go vet + go test
make dist      # cross-compiled binaries in dist/
```

Releases are built by GitHub Actions when a `v*` tag is pushed:

```bash
git tag v0.1.1 && git push origin v0.1.1
```
