package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func startHTTP() {
	mux := http.NewServeMux()

	xmlHandler := func(body func() string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/xml; charset=utf-8")
			io.WriteString(w, body())
		}
	}

	mux.HandleFunc("/description.xml", xmlHandler(deviceDescriptionXML))
	mux.HandleFunc("/scpd/avt", xmlHandler(avtSCPDXML))
	mux.HandleFunc("/scpd/rc", xmlHandler(rcSCPDXML))
	mux.HandleFunc("/scpd/cm", xmlHandler(cmSCPDXML))

	mux.HandleFunc("/ctrl/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			handleControl(w, r)
		case "SUBSCRIBE", "UNSUBSCRIBE":
			handleSubscribe(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/evt/", handleSubscribe)
	mux.HandleFunc("/sub", handleSubAPI)
	mux.HandleFunc("/status", handleStatus)

	startPort, err := strconv.Atoi(httpPort)
	if err != nil {
		startPort = 49152
	}

	var ln net.Listener
	for p := startPort; p < startPort+16; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			ln = l
			httpPort = strconv.Itoa(p)
			break
		}
		log.Printf("HTTP port %d busy: %v", p, err)
	}
	if ln == nil {
		log.Fatalf("could not bind any HTTP port in range %d..%d", startPort, startPort+15)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("HTTP server on %s:%s", currentLocalIP(), httpPort)
	if err := srv.Serve(ln); err != nil {
		log.Fatal("HTTP:", err)
	}
}

func handleSubAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "POST or PUT", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(r.FormValue("url"))
	if url == "" {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		url = strings.TrimSpace(string(body))
	}
	if url == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	mpvMu.Lock()
	currentSub = url
	mpvMu.Unlock()
	if mpvRunning() {
		if _, err := mpvCommand("sub-add", url, "select"); err != nil {
			log.Printf("sub-add failed: %v", err)
			http.Error(w, "sub-add failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("sub-add: %s", url)
	} else {
		log.Printf("sub queued for next play: %s", url)
	}
	io.WriteString(w, "ok\n")
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	mpvMu.Lock()
	cur, sub, nxt, nsub, queued := currentURL, currentSub, nextURL, nextSub, pendingStart
	mpvMu.Unlock()
	stateMu.Lock()
	state := transportState
	stateMu.Unlock()

	type status struct {
		DeviceName  string  `json:"device_name"`
		DeviceUUID  string  `json:"device_uuid"`
		LocalIP     string  `json:"local_ip"`
		HTTPPort    string  `json:"http_port"`
		MpvRunning  bool    `json:"mpv_running"`
		State       string  `json:"transport_state"`
		CurrentURL  string  `json:"current_url"`
		CurrentSub  string  `json:"current_sub,omitempty"`
		NextURL     string  `json:"next_url,omitempty"`
		NextSub     string  `json:"next_sub,omitempty"`
		Position    string  `json:"position"`
		Duration    string  `json:"duration"`
		PositionS   float64 `json:"position_seconds"`
		DurationS   float64 `json:"duration_seconds"`
		Volume      int     `json:"volume"`
		Mute        bool    `json:"mute"`
		PendingSeek string  `json:"pending_seek,omitempty"`
	}

	st := status{
		DeviceName:  deviceName,
		DeviceUUID:  deviceUUID,
		LocalIP:     currentLocalIP(),
		HTTPPort:    httpPort,
		MpvRunning:  mpvRunning(),
		State:       state,
		CurrentURL:  cur,
		CurrentSub:  sub,
		NextURL:     nxt,
		NextSub:     nsub,
		Position:    "00:00:00",
		Duration:    "00:00:00",
		Volume:      100,
		PendingSeek: queued,
	}

	if st.MpvRunning {
		if v, ok := mpvGet("time-pos"); ok {
			if f, ok := v.(float64); ok {
				st.PositionS = f
				st.Position = formatHMS(f)
			}
		}
		if v, ok := mpvGet("duration"); ok {
			if f, ok := v.(float64); ok {
				st.DurationS = f
				st.Duration = formatHMS(f)
			}
		}
		if v, ok := mpvGet("volume"); ok {
			if f, ok := v.(float64); ok {
				st.Volume = int(f)
			}
		}
		if v, ok := mpvGet("mute"); ok {
			if b, ok := v.(bool); ok {
				st.Mute = b
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}
