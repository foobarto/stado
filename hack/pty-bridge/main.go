// stado-pty-bridge — a dev-only tool that spawns a child process under a
// PTY and bridges its I/O to a browser tab via WebSocket. The browser
// renders the PTY output using xterm.js, so a Claude / Codex / agent
// driving Chrome via CDP can "see" a real terminal session.
//
// Usage:
//
//	stado-pty-bridge -addr 127.0.0.1:7878
//	# then navigate Chrome to http://127.0.0.1:7878/
//	# the page lets you pick a command (default: "stado") and connect.
//
// NOT for production: no auth, no TLS, no rate limits, no input
// sanitisation beyond what the PTY itself enforces. Bind to loopback
// only.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// authToken is set at startup. All HTTP + WS requests must present
// this token via either the `Authorization: Bearer <token>` header or
// a `?token=<token>` query parameter. Constant-time comparison
// prevents timing-side-channel guessing.
//
// Threat model: the bridge happily spawns arbitrary processes with
// the operator's UID. Anything reachable on loopback — another local
// user, a sandboxed app, a tab on a malicious origin doing DNS
// rebinding — would otherwise have remote-code-execution against
// every dev session. The token raises the bar to "must read the URL
// the bridge printed at startup."
var authToken []byte

//go:embed index.html
var staticFS embed.FS

var upgrader = websocket.Upgrader{
	// Origin check: refuse cross-origin WebSocket upgrades. The
	// bridge listens on 127.0.0.1, so a legitimate browser session
	// will report Origin: http://127.0.0.1:<port>. A page on
	// http://evil.example.com trying to drive us via DNS rebinding
	// fails the origin check before the token is even consulted.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Some non-browser clients (curl, ws-cli) don't send
			// Origin. Accept those — the token check still gates
			// them.
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		host, _, err := net.SplitHostPort(u.Host)
		if err != nil {
			host = u.Host
		}
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	},
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// requireAuth wraps an http.Handler to gate it on the bearer token.
// Accepts the token via Authorization header OR ?token= query param;
// browsers loading a fresh page can't easily set headers, so the
// query form is necessary for the initial GET /. Subsequent fetches
// (including the WS upgrade) re-extract the token the page captured
// on first load.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var presented []byte
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			presented = []byte(strings.TrimPrefix(h, "Bearer "))
		} else if t := r.URL.Query().Get("token"); t != "" {
			presented = []byte(t)
		}
		// Constant-time comparison; subtle.ConstantTimeCompare returns
		// 1 iff lengths AND bytes match, otherwise 0.
		if len(presented) == 0 || subtle.ConstantTimeCompare(presented, authToken) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="stado-pty-bridge"`)
			http.Error(w, "401 unauthorized — append ?token=<hex> to the URL or set Authorization: Bearer <hex>", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// controlMsg is the JSON shape browsers send for non-keystroke
// signals (resize, etc.). Keystrokes ride binary frames so the
// PTY's input parser doesn't have to undo a JSON wrapper for every
// arrow key.
type controlMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cmd  string `json:"cmd,omitempty"`
	Args string `json:"args,omitempty"` // space-separated extra args
	Cwd  string `json:"cwd,omitempty"`
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	defer conn.Close()

	// Spawn config — the browser sends a `start` control message
	// before any I/O so the operator can pick a different cmd via
	// the URL bar without restarting the bridge.
	cfg, err := waitForStart(conn)
	if err != nil {
		log.Printf("start handshake: %v", err)
		return
	}

	args := strings.Fields(cfg.Args)
	cmd := exec.Command(cfg.Cmd, args...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	// Inherit env so the child sees PATH / config / locale; xterm.js
	// reports as "xterm-256color" but the browser-side init can override.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	winsize := &pty.Winsize{Cols: cfg.Cols, Rows: cfg.Rows}
	if winsize.Cols == 0 {
		winsize.Cols = 120
	}
	if winsize.Rows == 0 {
		winsize.Rows = 32
	}

	f, err := pty.StartWithSize(cmd, winsize)
	if err != nil {
		writeText(conn, "[bridge] pty start failed: "+err.Error())
		return
	}

	var once sync.Once
	cleanup := func() {
		_ = f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGHUP)
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	}
	defer once.Do(cleanup)

	// PTY → WS. Binary frames; the browser treats them as raw byte
	// streams xterm.js' `term.write(Uint8Array)` accepts.
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"exit"}`))
				once.Do(cleanup)
				return
			}
		}
	}()

	// WS → PTY. Text frames carry control JSON; binary frames carry
	// raw keystrokes.
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			if _, werr := f.Write(data); werr != nil {
				return
			}
		case websocket.TextMessage:
			var c controlMsg
			if err := json.Unmarshal(data, &c); err != nil {
				continue
			}
			switch c.Type {
			case "resize":
				_ = pty.Setsize(f, &pty.Winsize{Cols: c.Cols, Rows: c.Rows})
			case "kill":
				return
			}
		}
	}
}

// waitForStart blocks until the browser sends a `{"type":"start",...}`
// control message. Anything else (keystrokes, malformed JSON) is
// dropped — keeps the protocol explicit so accidental connects don't
// spawn anything.
func waitForStart(conn *websocket.Conn) (*controlMsg, error) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt != websocket.TextMessage {
			continue
		}
		var c controlMsg
		if err := json.Unmarshal(data, &c); err != nil {
			continue
		}
		if c.Type == "start" && c.Cmd != "" {
			return &c, nil
		}
	}
}

func writeText(conn *websocket.Conn, s string) {
	_ = conn.WriteMessage(websocket.TextMessage, []byte(s))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7878", "listen address (loopback only by default)")
	tokenFlag := flag.String("token", "", "bearer token (hex). Empty = generate a fresh 32-byte random token at startup.")
	flag.Parse()

	if !strings.HasPrefix(*addr, "127.0.0.1:") && !strings.HasPrefix(*addr, "localhost:") && !strings.HasPrefix(*addr, "[::1]:") {
		log.Printf("warning: -addr=%q is not loopback. Anyone on the network who learns the token can spawn processes as you.", *addr)
	}

	if *tokenFlag != "" {
		authToken = []byte(*tokenFlag)
	} else {
		raw := make([]byte, 32) // 256 bits
		if _, err := rand.Read(raw); err != nil {
			log.Fatalf("token generation failed: %v", err)
		}
		authToken = []byte(hex.EncodeToString(raw))
	}

	mux := http.NewServeMux()
	// Both / and /ws are gated by requireAuth. The HTML embeds the
	// token from the URL into its WebSocket connect call, so once
	// the operator opens the printed URL the rest of the session
	// flows transparently.
	mux.Handle("/ws", requireAuth(http.HandlerFunc(wsHandler)))
	sub, err := fs.Sub(staticFS, ".")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", requireAuth(http.FileServer(http.FS(sub))))

	log.Printf("stado-pty-bridge listening on http://%s", *addr)
	log.Printf("open this URL in your browser:")
	log.Printf("    http://%s/?token=%s", *addr, authToken)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
