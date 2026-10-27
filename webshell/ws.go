package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
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

// buildShellCmd returns the argv used to spawn the PTY shell.
// With the /data translation layer enabled the shell is wrapped in proot,
// so inside the terminal "/" is backed by <DataRoot>/rootfs and everything
// the user writes or installs there persists across container restarts.
//
// Notes on proot invocation (verified against proot 5.1.0 in Debian bookworm):
//   - "-R" (root + "recommended" bindings) mis-translates writes: /root
//     writes leaked to the host root. Use plain "-r" + explicit binds.
//   - proot 5.1.0 has no "--" separator; the program path must follow the
//     options directly (shell paths start with "/", so no ambiguity).
func buildShellCmd(cfg config) []string {
	if rootfs := cfg.RootFS(); rootfs != "" {
		return []string{
			"proot",
			"-r", rootfs,
			"-b", "/proc",
			"-b", "/dev",
			"-b", "/sys",
			"-b", "/etc/resolv.conf", // DNS is managed by the container runtime
			"-b", "/etc/hosts",
			"-b", "/etc/host.conf",
			"-b", "/etc/nsswitch.conf",
			"-w", "/root",
			cfg.ShellPath, "-l",
		}
	}
	return []string{cfg.ShellPath}
}

// handleWS upgrades to a websocket and bridges it to a PTY running the shell.
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

	argv := buildShellCmd(s.cfg)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME=/root")
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
