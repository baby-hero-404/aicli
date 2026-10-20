// Package wsgorilla serves capture sessions over WebSocket using gorilla/websocket.
//
// Wire protocol (server to client, JSON text frames):
//
//	{"type":"stdout","data":"..."}      terminal output
//	{"type":"error","message":"..."}    a session error
//	{"type":"exit","payload":{path:content}}  the session ended; payload holds collected files
//
// Client to server: raw text/binary frames are terminal input, except a frame starting with
// "\x00RESIZE:<cols>:<rows>" which resizes the PTY. NUL never appears in real keyboard input,
// so the control frame cannot collide with stdin.
package wsgorilla

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/gorilla/websocket"
)

// ResizePrefix starts an out-of-band terminal-resize control frame.
const ResizePrefix = "\x00RESIZE:"

const (
	closeWriteWait = 2 * time.Second
	// MaxFrameBytes is the largest client frame accepted. Keystrokes and pastes are small; a
	// limit stops a client from exhausting server memory with one huge frame.
	MaxFrameBytes = 1 << 20
)

// Conn adapts a WebSocket connection to capture.Terminal.
type Conn struct {
	ws     *websocket.Conn
	resize chan sandbox.TerminalSize
	// gorilla connections support one concurrent writer.
	wmu sync.Mutex

	rbuf []byte

	onDisconnect func()
	disconnect   sync.Once
}

// NewConn wraps ws. The caller keeps ownership of closing it via Close.
func NewConn(ws *websocket.Conn) *Conn {
	ws.SetReadLimit(MaxFrameBytes)
	return &Conn{ws: ws, resize: make(chan sandbox.TerminalSize, 4)}
}

// OnDisconnect registers fn to run once when the client's connection fails or closes. A
// sandboxed shell keeps running when its client goes away, so callers use this to cancel the
// session and release the container.
func (c *Conn) OnDisconnect(fn func()) { c.onDisconnect = fn }

func (c *Conn) disconnected() {
	c.disconnect.Do(func() {
		if c.onDisconnect != nil {
			c.onDisconnect()
		}
	})
}

// Stdin implements capture.Terminal.
func (c *Conn) Stdin() io.Reader { return &stdinReader{c: c} }

// Stdout implements capture.Terminal.
func (c *Conn) Stdout() io.Writer { return &stdoutWriter{c: c} }

// Resize implements capture.Terminal.
func (c *Conn) Resize() <-chan sandbox.TerminalSize { return c.resize }

// SendStdout writes a formatted message to the terminal output.
func (c *Conn) SendStdout(format string, args ...any) {
	_ = c.sendJSON(map[string]any{"type": "stdout", "data": fmt.Sprintf(format, args...)})
}

// SendError reports a session error to the client.
func (c *Conn) SendError(format string, args ...any) {
	_ = c.sendJSON(map[string]any{"type": "error", "message": fmt.Sprintf(format, args...)})
}

// SendExit tells the client the session ended and hands over the collected files.
func (c *Conn) SendExit(files map[string]string) error {
	return c.sendJSON(map[string]any{"type": "exit", "payload": files})
}

// Close performs the WebSocket closing handshake, so frames already written (notably the
// exit payload) are delivered before the connection is torn down, then closes the socket.
func (c *Conn) Close() error {
	c.wmu.Lock()
	_ = c.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(closeWriteWait))
	c.wmu.Unlock()
	return c.ws.Close()
}

func (c *Conn) sendJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, payload)
}

type stdinReader struct{ c *Conn }

// Read returns terminal input, transparently turning resize control frames into Resize events.
func (r *stdinReader) Read(p []byte) (int, error) {
	c := r.c
	if len(c.rbuf) > 0 {
		n := copy(p, c.rbuf)
		c.rbuf = c.rbuf[n:]
		return n, nil
	}
	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			c.disconnected()
			return 0, err
		}
		if rest, ok := strings.CutPrefix(string(msg), ResizePrefix); ok {
			var cols, rows uint
			if _, err := fmt.Sscanf(rest, "%d:%d", &cols, &rows); err == nil {
				select {
				case c.resize <- sandbox.TerminalSize{Cols: cols, Rows: rows}:
				default: // drop when the consumer is behind; the next resize supersedes it
				}
			}
			continue
		}
		n := copy(p, msg)
		if n < len(msg) {
			c.rbuf = msg[n:]
		}
		return n, nil
	}
}

type stdoutWriter struct{ c *Conn }

// Write wraps PTY output in a JSON "stdout" frame so the client can tell it from control frames.
func (w *stdoutWriter) Write(p []byte) (int, error) {
	if err := w.c.sendJSON(map[string]any{"type": "stdout", "data": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}
