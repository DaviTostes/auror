package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type mpvIPC struct {
	mu      sync.Mutex // serializes writes to conn
	conn    net.Conn
	nextID  atomic.Uint64
	pending sync.Map // uint64 → chan map[string]any
	closeMu sync.Mutex
	closed  bool
}

var (
	mpvIPCConn *mpvIPC
	mpvIPCMu   sync.Mutex
)

func ensureMpvIPC() (*mpvIPC, error) {
	mpvIPCMu.Lock()
	defer mpvIPCMu.Unlock()
	if mpvIPCConn != nil {
		mpvIPCConn.closeMu.Lock()
		alive := !mpvIPCConn.closed
		mpvIPCConn.closeMu.Unlock()
		if alive {
			return mpvIPCConn, nil
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	var err error
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("unix", mpvSocket, 200*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	ipc := &mpvIPC{conn: conn}
	mpvIPCConn = ipc
	go ipc.reader()
	return ipc, nil
}

func (c *mpvIPC) reader() {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if rid, ok := msg["request_id"]; ok {
			var id uint64
			if f, ok := rid.(float64); ok {
				id = uint64(f)
			}
			if v, ok := c.pending.LoadAndDelete(id); ok {
				v.(chan map[string]any) <- msg
			}
			continue
		}
		if event, _ := msg["event"].(string); event == "property-change" {
			handleMpvPropChange(msg)
		}
	}
	c.closeMu.Lock()
	c.closed = true
	c.closeMu.Unlock()
	c.conn.Close()
	c.pending.Range(func(k, v any) bool {
		close(v.(chan map[string]any))
		c.pending.Delete(k)
		return true
	})
	mpvIPCMu.Lock()
	if mpvIPCConn == c {
		mpvIPCConn = nil
	}
	mpvIPCMu.Unlock()
}

func (c *mpvIPC) call(args []any, timeout time.Duration) (map[string]any, error) {
	id := c.nextID.Add(1)
	ch := make(chan map[string]any, 1)
	c.pending.Store(id, ch)
	payload, err := json.Marshal(map[string]any{"command": args, "request_id": id})
	if err != nil {
		c.pending.Delete(id)
		return nil, err
	}
	c.mu.Lock()
	_, err = c.conn.Write(append(payload, '\n'))
	c.mu.Unlock()
	if err != nil {
		c.pending.Delete(id)
		return nil, err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mpv ipc connection closed")
		}
		return resp, nil
	case <-time.After(timeout):
		c.pending.Delete(id)
		return nil, fmt.Errorf("mpv ipc timeout")
	}
}

func mpvCommand(args ...any) (map[string]any, error) {
	ipc, err := ensureMpvIPC()
	if err != nil {
		return nil, err
	}
	return ipc.call(args, 2*time.Second)
}

func mpvGet(prop string) (any, bool) {
	resp, err := mpvCommand("get_property", prop)
	if err != nil {
		return nil, false
	}
	if resp["error"] != "success" {
		return nil, false
	}
	return resp["data"], true
}

func mpvRunning() bool {
	mpvMu.Lock()
	defer mpvMu.Unlock()
	return mpvCmd != nil && mpvCmd.Process != nil
}

// subscribeMpvObservers registers property observers on the persistent IPC
// connection. Events flow back through the reader → handleMpvPropChange.
func subscribeMpvObservers() {
	props := []struct {
		id   int
		name string
	}{
		{1, "path"},
		{2, "pause"},
		{3, "volume"},
		{4, "mute"},
	}
	for _, p := range props {
		if _, err := mpvCommand("observe_property", p.id, p.name); err != nil {
			log.Printf("observe_property %s failed: %v", p.name, err)
		}
	}
}

func handleMpvPropChange(msg map[string]any) {
	name, _ := msg["name"].(string)
	switch name {
	case "path":
		if p, ok := msg["data"].(string); ok && p != "" {
			handlePathChange(p)
		}
	case "pause":
		if b, ok := msg["data"].(bool); ok {
			if b {
				setTransportState("PAUSED_PLAYBACK")
			} else if mpvRunning() {
				setTransportState("PLAYING")
			}
		}
	case "volume", "mute":
		fireEvent("rc")
	}
}

func handlePathChange(p string) {
	mpvMu.Lock()
	if currentURL == p {
		mpvMu.Unlock()
		return
	}
	log.Printf("path changed → %s", p)
	currentURL = p
	playingURL = p
	if p == nextURL {
		nextURL = ""
		nextSub = ""
	}
	mpvMu.Unlock()
	fireEvent("avt")
}

// playURL orchestrates playback. Three branches: resume same URL, swap in
// running mpv, spawn fresh mpv. Caller holds no locks.
func playURL(url, subURL string) {
	if url == "" {
		return
	}

	mpvMu.Lock()
	same := mpvCmd != nil && mpvCmd.Process != nil && playingURL == url
	running := mpvCmd != nil && mpvCmd.Process != nil
	start := pendingStart
	pendingStart = ""
	if !same {
		currentURL = url
		playingURL = url
	}
	mpvMu.Unlock()

	if same {
		resumeSameURL(start)
		return
	}

	if running {
		if err := swapInPlace(url, subURL, start); err == nil {
			setTransportState("PLAYING")
			return
		} else {
			log.Printf("swap failed: %v — respawning mpv", err)
			killMpv()
			mpvMu.Lock()
			pendingStart = start
			mpvMu.Unlock()
		}
	}

	if err := spawnMpv(url, subURL); err != nil {
		log.Println("mpv spawn error:", err)
		return
	}
	setTransportState("PLAYING")
}

func resumeSameURL(start string) {
	if start != "" {
		if secs, err := parseHMS(start); err == nil {
			if resp, err := mpvCommand("seek", secs, "absolute"); err != nil {
				log.Printf("resume seek failed: %v", err)
			} else {
				log.Printf("resume seek → %s: %v", start, resp["error"])
			}
		}
	}
	if resp, err := mpvCommand("set_property", "pause", false); err != nil {
		log.Printf("unpause failed: %v", err)
	} else {
		log.Printf("unpause: %v", resp["error"])
	}
	setTransportState("PLAYING")
}

func swapInPlace(url, subURL, start string) error {
	fmt.Printf("\n▶ swap: %s\n", url)
	loadArgs := []any{"loadfile", url, "replace"}
	if start != "" {
		loadArgs = append(loadArgs, 0, "start="+start)
	}
	if _, err := mpvCommand(loadArgs...); err != nil {
		return err
	}
	if subURL != "" {
		if _, err := mpvCommand("sub-add", subURL, "select"); err != nil {
			log.Printf("sub-add failed: %v", err)
		}
	}
	return nil
}

func killMpv() {
	mpvMu.Lock()
	if mpvCmd != nil && mpvCmd.Process != nil {
		mpvCmd.Process.Kill()
		mpvCmd = nil
	}
	mpvMu.Unlock()
}

func spawnMpv(url, subURL string) error {
	os.Remove(mpvSocket)
	fmt.Printf("\n▶ playing: %s\n", url)
	if subURL != "" {
		fmt.Printf("   sub    : %s\n", subURL)
	}
	args := []string{
		url,
		"--input-ipc-server=" + mpvSocket,
		"--idle=yes",
		"--force-window=yes",
		"--keep-open=yes",
	}
	if subURL != "" {
		args = append(args, "--sub-file="+subURL)
	}
	mpvMu.Lock()
	if pendingStart != "" {
		args = append(args, "--start="+pendingStart)
		fmt.Printf("   start  : %s\n", pendingStart)
		pendingStart = ""
	}
	mpvMu.Unlock()

	cmd := exec.Command(mpvBinary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		mpvMu.Lock()
		playingURL = ""
		mpvMu.Unlock()
		return err
	}
	mpvMu.Lock()
	mpvCmd = cmd
	mpvMu.Unlock()

	go subscribeMpvObservers()
	go waitMpvExit(cmd)
	return nil
}

func waitMpvExit(cmd *exec.Cmd) {
	cmd.Wait()
	mpvMu.Lock()
	wasOurs := mpvCmd == cmd
	if wasOurs {
		mpvCmd = nil
		playingURL = ""
		currentURL = ""
		nextURL = ""
	}
	mpvMu.Unlock()
	if wasOurs {
		setTransportState("STOPPED")
	}
}

func stopPlayback() {
	mpvMu.Lock()
	if mpvCmd != nil && mpvCmd.Process != nil {
		mpvCmd.Process.Kill()
		mpvCmd = nil
	}
	currentURL = ""
	currentSub = ""
	pendingStart = ""
	playingURL = ""
	nextURL = ""
	nextSub = ""
	mpvMu.Unlock()
	setTransportState("STOPPED")
}
