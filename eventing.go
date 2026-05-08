package main

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type subscription struct {
	sid       string
	service   string // "avt", "rc", "cm"
	callbacks []string
	seq       uint32
	expires   time.Time
	sendMu    sync.Mutex // serialize NOTIFYs to preserve SEQ order
}

var (
	subsMu  sync.Mutex
	subs    = map[string]map[string]*subscription{} // service → sid → sub
	sidSeed atomic.Uint64
)

func parseCallbacks(h string) []string {
	var out []string
	for {
		i := strings.Index(h, "<")
		if i == -1 {
			break
		}
		j := strings.Index(h[i:], ">")
		if j == -1 {
			break
		}
		out = append(out, h[i+1:i+j])
		h = h[i+j+1:]
	}
	return out
}

func parseTimeout(h string) time.Duration {
	h = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(h), "second-"))
	if h == "" || h == "infinite" {
		return 1800 * time.Second
	}
	n, err := strconv.Atoi(h)
	if err != nil || n <= 0 {
		return 1800 * time.Second
	}
	return time.Duration(n) * time.Second
}

func serviceFromPath(p string) string {
	switch {
	case strings.Contains(p, "/avt"):
		return "avt"
	case strings.Contains(p, "/rc"):
		return "rc"
	case strings.Contains(p, "/cm"):
		return "cm"
	}
	return ""
}

func avtLastChange() string {
	stateMu.Lock()
	state := transportState
	stateMu.Unlock()
	mpvMu.Lock()
	uri := currentURL
	nxt := nextURL
	mpvMu.Unlock()
	pos, dur := "00:00:00", "00:00:00"
	if mpvRunning() {
		if v, ok := mpvGet("time-pos"); ok {
			if f, ok := v.(float64); ok {
				pos = formatHMS(f)
			}
		}
		if v, ok := mpvGet("duration"); ok {
			if f, ok := v.(float64); ok {
				dur = formatHMS(f)
			}
		}
	}
	return fmt.Sprintf(`&lt;Event xmlns=&quot;urn:schemas-upnp-org:metadata-1-0/AVT/&quot;&gt;&lt;InstanceID val=&quot;0&quot;&gt;`+
		`&lt;TransportState val=&quot;%s&quot;/&gt;&lt;TransportStatus val=&quot;OK&quot;/&gt;`+
		`&lt;CurrentTrackURI val=&quot;%s&quot;/&gt;&lt;AVTransportURI val=&quot;%s&quot;/&gt;`+
		`&lt;NextAVTransportURI val=&quot;%s&quot;/&gt;&lt;TransportPlaySpeed val=&quot;1&quot;/&gt;`+
		`&lt;CurrentTrackDuration val=&quot;%s&quot;/&gt;&lt;CurrentMediaDuration val=&quot;%s&quot;/&gt;`+
		`&lt;RelativeTimePosition val=&quot;%s&quot;/&gt;&lt;AbsoluteTimePosition val=&quot;%s&quot;/&gt;`+
		`&lt;/InstanceID&gt;&lt;/Event&gt;`,
		state, html.EscapeString(uri), html.EscapeString(uri),
		html.EscapeString(nxt), dur, dur, pos, pos)
}

func rcLastChange() string {
	vol := 100
	mute := 0
	if mpvRunning() {
		if v, ok := mpvGet("volume"); ok {
			if f, ok := v.(float64); ok {
				vol = int(f)
			}
		}
		if v, ok := mpvGet("mute"); ok {
			if b, ok := v.(bool); ok && b {
				mute = 1
			}
		}
	}
	return fmt.Sprintf(`&lt;Event xmlns=&quot;urn:schemas-upnp-org:metadata-1-0/RCS/&quot;&gt;&lt;InstanceID val=&quot;0&quot;&gt;&lt;Mute channel=&quot;Master&quot; val=&quot;%d&quot;/&gt;&lt;Volume channel=&quot;Master&quot; val=&quot;%d&quot;/&gt;&lt;/InstanceID&gt;&lt;/Event&gt;`, mute, vol)
}

func buildPropertySet(service string) string {
	var body string
	switch service {
	case "avt":
		body = fmt.Sprintf(`<e:property><LastChange>%s</LastChange></e:property>`, avtLastChange())
	case "rc":
		body = fmt.Sprintf(`<e:property><LastChange>%s</LastChange></e:property>`, rcLastChange())
	case "cm":
		body = fmt.Sprintf(`<e:property><SourceProtocolInfo></SourceProtocolInfo></e:property><e:property><SinkProtocolInfo>%s</SinkProtocolInfo></e:property><e:property><CurrentConnectionIDs>0</CurrentConnectionIDs></e:property>`, sinkProtocolInfo)
	}
	return `<?xml version="1.0"?>` + "\n" +
		`<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">` + body + `</e:propertyset>`
}

var notifyClient = &http.Client{Timeout: 5 * time.Second}

// notifySub serializes NOTIFYs per subscription so SEQ order is preserved.
func notifySub(sub *subscription, body string) {
	sub.sendMu.Lock()
	defer sub.sendMu.Unlock()
	subsMu.Lock()
	seq := sub.seq
	sub.seq++
	subsMu.Unlock()
	for _, cb := range sub.callbacks {
		req, err := http.NewRequest("NOTIFY", cb, strings.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
		req.Header.Set("NT", "upnp:event")
		req.Header.Set("NTS", "upnp:propchange")
		req.Header.Set("SID", sub.sid)
		req.Header.Set("SEQ", strconv.FormatUint(uint64(seq), 10))
		resp, err := notifyClient.Do(req)
		if err != nil {
			log.Printf("NOTIFY %s seq=%d failed: %v", cb, seq, err)
			continue
		}
		resp.Body.Close()
	}
}

func fireEvent(service string) {
	body := buildPropertySet(service)
	subsMu.Lock()
	var targets []*subscription
	if m, ok := subs[service]; ok {
		now := time.Now()
		for sid, s := range m {
			if now.After(s.expires) {
				delete(m, sid)
				continue
			}
			targets = append(targets, s)
		}
	}
	subsMu.Unlock()
	for _, s := range targets {
		go notifySub(s, body)
	}
}

func sweepSubscriptions() {
	ticker := time.NewTicker(60 * time.Second)
	for range ticker.C {
		subsMu.Lock()
		now := time.Now()
		removed := 0
		for _, m := range subs {
			for sid, s := range m {
				if now.After(s.expires) {
					delete(m, sid)
					removed++
				}
			}
		}
		subsMu.Unlock()
		if removed > 0 {
			log.Printf("subscription sweep: %d expired removed", removed)
		}
	}
}

func setTransportState(s string) {
	stateMu.Lock()
	changed := transportState != s
	transportState = s
	stateMu.Unlock()
	if changed {
		log.Printf("transport state → %s", s)
		fireEvent("avt")
	}
}

func startPositionEvents() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		stateMu.Lock()
		s := transportState
		stateMu.Unlock()
		if s == "PLAYING" {
			fireEvent("avt")
		}
	}
}

func handleSubscribe(w http.ResponseWriter, r *http.Request) {
	service := serviceFromPath(r.URL.Path)
	if service == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if r.Method == "UNSUBSCRIBE" {
		sid := r.Header.Get("SID")
		subsMu.Lock()
		if m, ok := subs[service]; ok {
			delete(m, sid)
		}
		subsMu.Unlock()
		w.WriteHeader(200)
		return
	}

	timeout := parseTimeout(r.Header.Get("TIMEOUT"))

	if sid := r.Header.Get("SID"); sid != "" {
		subsMu.Lock()
		if m, ok := subs[service]; ok {
			if s, ok := m[sid]; ok {
				s.expires = time.Now().Add(timeout)
			}
		}
		subsMu.Unlock()
		w.Header().Set("SID", sid)
		w.Header().Set("TIMEOUT", fmt.Sprintf("Second-%d", int(timeout.Seconds())))
		w.WriteHeader(200)
		return
	}

	callbacks := parseCallbacks(r.Header.Get("CALLBACK"))
	if len(callbacks) == 0 {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}

	sid := fmt.Sprintf("uuid:sub-%d", sidSeed.Add(1))
	sub := &subscription{
		sid:       sid,
		service:   service,
		callbacks: callbacks,
		seq:       0,
		expires:   time.Now().Add(timeout),
	}
	subsMu.Lock()
	if subs[service] == nil {
		subs[service] = map[string]*subscription{}
	}
	subs[service][sid] = sub
	subsMu.Unlock()

	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", fmt.Sprintf("Second-%d", int(timeout.Seconds())))
	w.Header().Set("Server", "Linux/1.0 UPnP/1.0 go-dlna/1.0")
	w.WriteHeader(200)

	go notifySub(sub, buildPropertySet(service))
}
