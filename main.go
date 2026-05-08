package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	httpPort   = "49152"
	deviceName = "auror"
	mpvBinary  = "mpv"
	mpvSocket  = fmt.Sprintf("/tmp/auror-mpv-%d.sock", os.Getpid())
	deviceUUID = "uuid:11111111-2222-3333-4444-555555555555"

	mpvCmd       *exec.Cmd
	mpvMu        sync.Mutex
	currentURL   string
	currentSub   string
	pendingStart string
	playingURL   string
	nextURL      string
	nextSub      string

	stateMu        sync.Mutex
	transportState = "NO_MEDIA_PRESENT"
)

func main() {
	flag.StringVar(&httpPort, "port", httpPort, "HTTP port (will walk +15 if busy)")
	flag.StringVar(&deviceName, "name", deviceName, "DLNA friendly name")
	flag.StringVar(&mpvBinary, "mpv", mpvBinary, "mpv binary path")
	uuidOverride := flag.String("uuid", "", "device UUID override (default: persist in user config dir)")
	flag.Parse()

	if *uuidOverride != "" {
		if !strings.HasPrefix(*uuidOverride, "uuid:") {
			deviceUUID = "uuid:" + *uuidOverride
		} else {
			deviceUUID = *uuidOverride
		}
	} else {
		deviceUUID = loadOrCreateUUID()
	}

	sidSeed.Store(uint64(time.Now().UnixNano()))
	localIP = getLocalIP()

	fmt.Printf("auror starting\n")
	fmt.Printf("  device name : %s\n", deviceName)
	fmt.Printf("  device UUID : %s\n", deviceUUID)
	fmt.Printf("  local IP    : %s\n", localIP)
	fmt.Printf("  HTTP port   : %s\n", httpPort)
	fmt.Printf("  mpv binary  : %s\n", mpvBinary)
	fmt.Printf("  mpv socket  : %s\n", mpvSocket)
	fmt.Printf("  description : http://%s:%s/description.xml\n", localIP, httpPort)
	fmt.Printf("  status      : http://%s:%s/status\n\n", localIP, httpPort)
	fmt.Println("waiting for DLNA controller to connect...")

	go startHTTP()
	go startSSDP()
	go sweepSubscriptions()
	go startPositionEvents()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nshutting down...")
	byebye()
	stopPlayback()
	os.Remove(mpvSocket)
}
