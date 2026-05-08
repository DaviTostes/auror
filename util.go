package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	localIPMu sync.RWMutex
	localIP   string
)

func getLocalIP() string {
	conn, err := net.Dial("udp4", "239.255.255.250:1900")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func currentLocalIP() string {
	localIPMu.RLock()
	defer localIPMu.RUnlock()
	return localIP
}

func refreshLocalIP() (changed bool, old, new string) {
	new = getLocalIP()
	localIPMu.Lock()
	old = localIP
	if new != "" && new != "127.0.0.1" && new != localIP {
		localIP = new
		changed = true
	}
	localIPMu.Unlock()
	return
}

func loadOrCreateUUID() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	dir = filepath.Join(dir, "auror")
	path := filepath.Join(dir, "uuid")
	if data, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(data))
		if strings.HasPrefix(s, "uuid:") {
			return s
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("uuid dir create failed: %v — using ephemeral", err)
		return "uuid:" + randomUUIDv4()
	}
	uuid := "uuid:" + randomUUIDv4()
	if err := os.WriteFile(path, []byte(uuid+"\n"), 0o644); err != nil {
		log.Printf("uuid write failed: %v", err)
	}
	return uuid
}

func randomUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x-%016x", time.Now().UnixNano(), os.Getpid())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func formatHMS(seconds float64) string {
	if seconds < 0 || seconds != seconds {
		return "00:00:00"
	}
	s := int(seconds)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s/60)%60, s%60)
}

func parseHMS(s string) (float64, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	var h, m int
	var sec float64
	var err error
	switch len(parts) {
	case 3:
		if h, err = strconv.Atoi(parts[0]); err != nil {
			return 0, err
		}
		if m, err = strconv.Atoi(parts[1]); err != nil {
			return 0, err
		}
		if sec, err = strconv.ParseFloat(parts[2], 64); err != nil {
			return 0, err
		}
	case 2:
		if m, err = strconv.Atoi(parts[0]); err != nil {
			return 0, err
		}
		if sec, err = strconv.ParseFloat(parts[1], 64); err != nil {
			return 0, err
		}
	case 1:
		if sec, err = strconv.ParseFloat(parts[0], 64); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("bad time %q", s)
	}
	return float64(h*3600+m*60) + sec, nil
}
