# ego-bridge

Let coding agents on a remote server (over SSH) drive the **ego-browser running
on your own computer**: your logged-in sessions, your profile, your screen.

```
 remote server                                   your Mac
┌──────────────────────────┐   ssh -R (unix     ┌───────────────────────────────┐
│ agent                    │   socket tunnel)   │ ego-bridge (in `ego-bridge ssh`│
│  └─ ego-browser nodejs … │ ─────────────────▶ │   or `ego-bridge serve`)      │
│     (ego-bridge shim)    │ ◀───────────────── │  └─ real ego-browser nodejs … │
│  files under $HOME, /tmp │  stdout/stderr,    │      └─ ego lite browser      │
│                          │  exit code, files  │                               │
└──────────────────────────┘                    └───────────────────────────────┘
```

## Why this works for every ego-browser feature

The agent-facing ego-browser interface is a CLI: `ego-browser nodejs` runs a
JavaScript script against the browser, `ego-browser skill` / `help` print docs.
Each call is a new process; task spaces, tabs and page labels persist in the
browser. So the shim on the server forwards **argv + stdin**, and streams back
**stdout, stderr and the exit code**. Task spaces, pages, snapshots, `evaluate`,
`cdp`, `fetch`, popups, dialogs, `handOff`/`takeOverTaskSpace` and
`--ego-server-name` all work unchanged.

The one thing that doesn't carry over by itself is **file paths**, because the
script runs on your Mac. ego-bridge handles that:

| Script does (on server) | What happens |
| --- | --- |
| `page.screenshot({ path: "/home/me/shots/a.png" })` | Path is rewritten to a per-run staging dir on the Mac; the PNG is copied back to `/home/me/shots/a.png` |
| `page.screenshot()` (no path) | The Mac temp file is copied back to `/tmp/ego-bridge/…` and the printed path is rewritten |
| `page.setInputFiles("#f", ["/home/me/report.pdf"])` | The server file is uploaded to staging before the run |
| `download.saveAs("/tmp/x.csv")`, `page.fetch(u, { saveAs })`, `fs.writeFile` | Copied back after the run |
| `page.goto("file:///home/me/site/index.html")` | The referenced file is uploaded and the URL rewritten |

Mapped dirs default to the server's `$HOME`, `/tmp`, `$TMPDIR` and the cwd
(override with `EGO_BRIDGE_ROOTS=/a:/b`). Paths must appear **literally** in the
script to be uploaded. Outputs are found by sweeping staging, so paths built at
runtime under a literal mapped dir work too. The generated skill tells agents
this.

## Setup

```bash
# On your Mac (needs ego lite installed)
make install                       # -> ~/.local/bin/ego-bridge

# Put it on the server and install the shim and skill
make deploy HOST=myserver          # ARCH=arm64 for ARM servers
# (manual: scp dist/ego-bridge-linux-amd64 myserver:.local/bin/ego-bridge
#          ssh myserver '~/.local/bin/ego-bridge install')
```

`ego-bridge install` (on the server) creates `~/.local/bin/ego-browser` →
`ego-bridge`, and writes the ego-browser skill to `~/.claude/skills/ego-browser`
and `~/.agents/skills/ego-browser` (Codex).

## Use

```bash
# From your Mac, instead of `ssh myserver`:
ego-bridge ssh myserver
# then on the server
ego-bridge status
claude    # agents just use `ego-browser` as usual
```

`ego-bridge ssh` takes any ssh args. It starts a private bridge for that
session and adds `-R /tmp/ego-bridge-<user>-<id>.sock:<local socket>`. The shim
finds the newest live socket owned by your user, so several sessions,
tmux and reconnects all work. Stale sockets are cleaned up.

### Long-lived alternative (ssh config, VS Code Remote, mosh+ssh)

```bash
ego-bridge serve        # on the Mac, e.g. as a launchd agent
```
```sshconfig
# ~/.ssh/config on the Mac
Host myserver
  RemoteForward /tmp/ego-bridge-<server-user>.sock /Users/<you>/.ego-bridge/bridge.sock
```
If reconnecting fails with "remote port forwarding failed", a stale socket
from the last session is still there. Either set `StreamLocalBindUnlink yes` in
the server's `sshd_config`, or use `ego-bridge ssh`, which avoids this with unique names.

## Security model: read this

A bridged agent can run **arbitrary JavaScript in Node on your Mac** (ego-browser's
runtime has full `node:fs`, `child_process`, network), plus your logged-in
browser. That is the same power the agent would have locally, but it now comes
from the server. So:

- Only bridge to servers you trust as much as your laptop. Anyone with root on the
  server can use the socket while you're connected.
- The socket on the server is `0600` (sshd's default `StreamLocalBindMask`), and
  the shim only connects to sockets owned by its own uid. Other users on the
  server can't reach it.
- Nothing listens on TCP. The bridge exists only while your SSH session (or
  `RemoteForward`) is up.
- `ego-browser upgrade` and `import` are refused over the bridge
  (`serve --allow-all-commands` to allow).

## Limits

- Script stdout reaches the server as ego-browser emits it (line-buffered).
- Files over 256 MiB are not transferred.
- Unix sockets only (macOS/Linux). Socket paths are limited to ~104 chars on macOS.
