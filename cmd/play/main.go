package main

// personal-game client shell: library, play, history. Media stays in
// Moonlight; this shell only orchestrates sessions and prints history.
//
//	ingress: go run ./cmd/play library
//	         go run ./cmd/play play --game <id> [--print-only]
//	         go run ./cmd/play history
//
// play creates the session, waits for assignment/preparation/READY,
// launches Moonlight automatically when present (verified
// `moonlight stream <host> "<app>"`), keeps state synchronized, closes
// the session on exit, and prints final playtime.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/personal-game/personal-game/pkg/protocol"
)

var client = &http.Client{Timeout: 30 * time.Second}

// apiToken is the optional client-API bearer token (PG_API_TOKEN). It is
// attached as Authorization: Bearer on every request and never logged.
var apiToken = os.Getenv("PG_API_TOKEN")

func authorize(req *http.Request) {
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "library":
		os.Exit(cmdLibrary(os.Args[2:]))
	case "history":
		os.Exit(cmdHistory(os.Args[2:]))
	case "play":
		os.Exit(cmdPlay(os.Args[2:]))
	case "pair":
		os.Exit(cmdPair(os.Args[2:]))
	case "add-game":
		os.Exit(cmdAddGame(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `personal-game client shell
  play --game ID [--control URL] [--user U] [--print-only]   play a game
  pair --host HOST [--pin PIN] [--moonlight-bin B]           pair Moonlight once
  library [--control URL]                                     list games
  history [--control URL] [--user U]                          playtime
  add-game --game-id ID --package-type T --url U --exe E ...  write a manifest`)
}

func cmdLibrary(args []string) int {
	fs := flag.NewFlagSet("library", flag.ExitOnError)
	control := fs.String("control", "http://127.0.0.1:8080", "control plane base URL")
	_ = fs.Parse(args)
	req, _ := http.NewRequest(http.MethodGet, *control+"/v1/games", nil)
	authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "library:", err)
		return 1
	}
	defer resp.Body.Close()
	var out struct {
		Games []protocol.GameManifest `json:"games"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fmt.Fprintln(os.Stderr, "library:", err)
		return 1
	}
	if len(out.Games) == 0 {
		fmt.Println("no games in catalog (seed games/manifests/*.json, restart control plane)")
		return 0
	}
	for _, g := range out.Games {
		fmt.Printf("%-24s %-40s v%s [%s]\n", g.GameID, g.Name, g.Version, g.Acquisition.PackageType)
	}
	return 0
}

// cmdPair performs first-time Moonlight pairing (once per node, reused
// afterwards). It delegates to the verified Moonlight CLI
// (`moonlight pair <host> [--pin PIN]`); pairing secrets stay on the
// client machine, never in git. Without --pin, Moonlight shows a PIN to
// approve on the Wolf/Sunshine host.
func cmdPair(args []string) int {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	host := fs.String("host", "", "node address (Tailscale IP or hostname)")
	pin := fs.String("pin", "", "optional predefined 4-digit PIN")
	mlBin := fs.String("moonlight-bin", "moonlight", "moonlight binary")
	_ = fs.Parse(args)
	if *host == "" {
		fmt.Fprintln(os.Stderr, "pair: --host is required")
		return 2
	}
	if *pin != "" && (len(*pin) != 4 || !isDigits(*pin)) {
		fmt.Fprintln(os.Stderr, "pair: PIN must be 4 digits")
		return 2
	}
	if _, err := exec.LookPath(*mlBin); err != nil {
		fmt.Fprintln(os.Stderr, "pair: moonlight binary not found (install Moonlight first)")
		return 1
	}
	pargs := []string{"pair", *host}
	if *pin != "" {
		pargs = append(pargs, "--pin", *pin)
	}
	cmd := exec.Command(*mlBin, pargs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pair: moonlight pairing failed:", err)
		fmt.Fprintln(os.Stderr, "pair: approve the PIN on the Wolf host (its pair queue) or Sunshine PIN page, then retry")
		return 1
	}
	fmt.Println("paired with", *host, "(stored client-side by Moonlight; reuse for all future sessions)")
	return 0
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cmdHistory(args []string) int {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	control := fs.String("control", "http://127.0.0.1:8080", "control plane base URL")
	user := fs.String("user", "player1", "user id")
	_ = fs.Parse(args)
	printPlaytime(*control, *user)
	return 0
}

func cmdPlay(args []string) int {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	control := fs.String("control", "http://127.0.0.1:8080", "control plane base URL")
	user := fs.String("user", "player1", "user id")
	game := fs.String("game", "", "game id to play (required)")
	readyTimeout := fs.Duration("ready-timeout", 15*time.Minute, "how long to wait for READY")
	printOnly := fs.Bool("print-only", false, "print the Moonlight command instead of launching it")
	moonlightBin := fs.String("moonlight-bin", "moonlight", "moonlight binary")
	_ = fs.Parse(args)
	if *game == "" {
		fmt.Fprintln(os.Stderr, "play: --game is required")
		return 2
	}
	return run(*control, *user, *game, *readyTimeout, *printOnly, *moonlightBin)
}

func run(control, user, gameID string, readyTimeout time.Duration, printOnly bool, mlBin string) int {
	sess, err := createSession(control, user, gameID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "play:", err)
		return 1
	}
	fmt.Printf("session %s on node %s (state %s)\n", sess.SessionID, sess.NodeID, sess.State)
	fmt.Println("waiting for node: prepare -> restore -> backend -> READY...")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ready, err := waitState(ctx, control, sess.SessionID, protocol.SessionReady, readyTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "play:", err)
		_ = closeSession(control, sess.SessionID, sess.NodeID, sess.FenceToken, protocol.EndAborted)
		return 1
	}
	fmt.Printf("READY on %s via %s — streaming app %q\n", ready.Stream.Host, ready.Stream.Provider, ready.Stream.App)
	if _, err := exec.LookPath(mlBin); err != nil {
		fmt.Printf("moonlight not found — install it, then run:\n  moonlight stream %s %q\n",
			ready.Stream.Host, ready.Stream.App)
		fmt.Println("paste the command above; press Ctrl+C here afterwards to close the session")
		<-ctx.Done()
		_ = closeSession(control, sess.SessionID, sess.NodeID, sess.FenceToken, protocol.EndGameExit)
		printPlaytime(control, user)
		return 0
	}
	if printOnly {
		fmt.Printf("moonlight stream %s %q\n", ready.Stream.Host, ready.Stream.App)
		fmt.Println("press Ctrl+C here to close the session when done")
		<-ctx.Done()
		_ = closeSession(control, sess.SessionID, sess.NodeID, sess.FenceToken, protocol.EndGameExit)
		printPlaytime(control, user)
		return 0
	}
	ml := exec.Command(mlBin, "stream", ready.Stream.Host, ready.Stream.App)
	ml.Stdin, ml.Stdout, ml.Stderr = os.Stdin, os.Stdout, os.Stderr
	fmt.Println("Launching Moonlight... (media flows directly client <-> node)")
	if err := ml.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "play: moonlight exited:", err)
	}
	_ = closeSession(control, sess.SessionID, sess.NodeID, sess.FenceToken, protocol.EndGameExit)
	printPlaytime(control, user)
	return 0
}

func createSession(control, user, gameID string) (*protocol.Session, error) {
	raw, _ := json.Marshal(map[string]string{"user_id": user, "game_id": gameID})
	req, _ := http.NewRequest(http.MethodPost, control+"/v1/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("create session: status %d: %s", resp.StatusCode, body)
	}
	var s protocol.Session
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// waitState polls until the session reaches (or passes) want. Terminal
// states before want fail loudly instead of hanging. Each newly observed
// state prints a user-facing progress line (no tokens or internals leak).
func waitState(ctx context.Context, control, id string, want protocol.SessionState, timeout time.Duration) (*protocol.Session, error) {
	started := time.Now()
	deadline := started.Add(timeout)
	last := protocol.SessionState("")
	for {
		s, err := getSession(control, id)
		if err != nil {
			return nil, err
		}
		if s.State != last {
			printStage(s.State, time.Since(started))
			last = s.State
		}
		if s.State == want || s.State == protocol.SessionStreaming {
			return s, nil
		}
		if s.State.IsTerminal() || s.State == protocol.SessionFenced {
			return nil, fmt.Errorf("session ended as %s before READY", s.State)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for READY (still %s); is the node agent running?", s.State)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("interrupted")
		case <-time.After(3 * time.Second):
		}
	}
}

// printStage maps internal states to the user-visible journey.
func printStage(s protocol.SessionState, elapsed time.Duration) {
	msg := map[protocol.SessionState]string{
		protocol.SessionRequested:    "Creating session...",
		protocol.SessionNodeAssigned: "Node assigned, preparing...",
		protocol.SessionPreparing:    "Checking game cache, restoring save, starting backend...",
		protocol.SessionReady:        "Ready.",
		protocol.SessionStreaming:    "Streaming.",
		protocol.SessionDegraded:     "Connection degraded, holding game...",
		protocol.SessionDraining:     "Finishing up...",
	}[s]
	if msg == "" {
		msg = fmt.Sprintf("State %s...", s)
	}
	fmt.Printf("[+%s] %s\n", elapsed.Round(time.Second), msg)
}

func getSession(control, id string) (*protocol.Session, error) {
	req, _ := http.NewRequest(http.MethodGet, control+"/v1/sessions/"+id, nil)
	authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s protocol.Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func closeSession(control, id, nodeID string, token uint64, reason protocol.SessionEndReason) error {
	req, _ := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/v1/sessions/%s?node_id=%s&fence_token=%d&reason=%s",
			control, id, nodeID, token, reason), nil)
	authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func printPlaytime(control, user string) {
	req, _ := http.NewRequest(http.MethodGet, control+"/v1/stats/playtime?user_id="+user, nil)
	authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var out struct {
		Playtime []map[string]any `json:"playtime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return
	}
	for _, p := range out.Playtime {
		fmt.Printf("playtime: game=%v active=%vs sessions=%v last=%v\n",
			p["game_id"], p["total_active_seconds"], p["sessions"], p["last_played"])
	}
}
