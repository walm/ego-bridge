package main

// Wire protocol: newline-delimited JSON over a unix socket.
//
// The client (the `ego-browser` shim on the remote server) sends exactly one
// Request, then keeps the connection open while the server streams Frames.
// Closing the connection early cancels the run: the server kills the local
// ego-browser process group.

const protocolVersion = 1

// FileData is a file transferred between the remote server and the Mac.
// Root indexes Request.Roots; Path is relative to that root.
type FileData struct {
	Root int    `json:"root"`
	Path string `json:"path"`
	Data []byte `json:"data,omitempty"`
	Mode uint32 `json:"mode,omitempty"`
}

type Request struct {
	Version int `json:"version"`
	// Args is the ego-browser argv (without the program name).
	Args []string `json:"args"`
	// Stdin is the script for `ego-browser nodejs < script.js`.
	Stdin    []byte `json:"stdin,omitempty"`
	HasStdin bool   `json:"has_stdin,omitempty"`
	// Roots are remote absolute directories whose paths are mapped to a
	// per-request staging directory on the Mac.
	Roots []string `json:"roots"`
	// CaptureRoot is the Roots index that receives local temp files that the
	// run prints (e.g. a screenshot() without a path). -1 disables capture.
	CaptureRoot int `json:"capture_root"`
	// Files are remote input files referenced by the script.
	Files []FileData `json:"files,omitempty"`
	// Dirs are remote directories referenced by the script; they are created
	// in staging so scripts can write into them.
	Dirs []FileData `json:"dirs,omitempty"`
}

type Frame struct {
	Type    string    `json:"type"` // stdout | stderr | file | exit | error
	Data    []byte    `json:"data,omitempty"`
	File    *FileData `json:"file,omitempty"`
	Code    int       `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
}
