package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// The session cookie is SameSite=Strict, which already pins the browser
	// to this origin; websocket terminal clients are not cookie-based.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// controlMsg is a JSON text frame sent by the browser.
// Binary frames are raw terminal input and are never JSON.
type controlMsg struct {
	Action string `json:"action"` // "resize"
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
}

// handleWS upgrades to a websocket and bridges it to a PTY running the shell.
// With the translate layer active, libdvps-t.so (global via /etc/ld.so.preload)
// rewrites whitelisted paths to <DATA_ROOT>/rootfs, so everything the user
// writes or installs persists across container restarts while the container
// root stays a real, symlink-free filesystem.
func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already replied with an HTTP error
	}
	defer ws.Close()

	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}

	cmd := exec.Command(s.cfg.ShellPath)
	env := append(os.Environ(), "TERM=xterm-256color", "HOME=/root")
	if s.cfg.DataRoot != "" {
		// T-layer: /etc/ld.so.preload already loads libdvps-t.so globally;
		// LD_PRELOAD is the belt-and-suspenders fallback. LD_LIBRARY_PATH
		// prefers volume-side libraries: the kernel loads the image-layer
		// ld-linux (PT_INTERP bypasses interception), so without this a
		// freshly installed binary needing newer glibc symbols could resolve
		// against the image's older libc.
		vroot := s.cfg.PersistRoot()
		llp := filepath.Join(vroot, "usr", "lib", "x86_64-linux-gnu") + ":" + filepath.Join(vroot, "usr", "lib", "x86_64-linux-gnu", "systemd") + ":" + filepath.Join(vroot, "usr", "lib")
		if old := os.Getenv("LD_LIBRARY_PATH"); old != "" {
			llp += ":" + old
		}
		env = append(env, "LD_PRELOAD=/usr/lib/libdvps-t.so", "LD_LIBRARY_PATH="+llp)
	}
	cmd.Env = env
	cmd.Dir = home

	ptmx, err := pty.Start(cmd) // spawn the shell attached to a PTY
	if err != nil {
		msg := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "cannot start shell")
		_ = ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
		return
	}
	defer ptmx.Close()

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = cmd.Wait()
	}()

	var writeMu sync.Mutex

	// PTY output -> websocket (binary frames)
	go func() {
		buf := make([]byte, 8192)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_ = ws.WriteMessage(websocket.BinaryMessage, buf[:n])
				writeMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	// websocket -> PTY; text frames are control messages, binary is raw input
	for {
		msgType, data, rerr := ws.ReadMessage()
		if rerr != nil {
			break
		}
		if msgType == websocket.TextMessage {
			var c controlMsg
			if json.Unmarshal(data, &c) == nil && c.Action == "resize" && c.Cols > 0 && c.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c.Cols), Rows: uint16(c.Rows)})
			}
			continue
		}
		if _, werr := ptmx.Write(data); werr != nil {
			break
		}
	}

	// tear the session down: closing the PTY master SIGHUPs the shell
	_ = ptmx.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
	}
}
