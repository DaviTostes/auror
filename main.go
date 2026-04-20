package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ssdpMulticast = "239.255.255.250:1900"
	deviceUUID    = "uuid:11111111-2222-3333-4444-555555555555"
	deviceName    = "auror"
	mpvSocket     = "/tmp/auror-mpv.sock"
)

var (
	httpPort     = "49152"
	localIP      string
	mpvCmd       *exec.Cmd
	mpvMu        sync.Mutex
	currentURL   string
	currentSub   string
	pendingStart string
	playingURL   string
)

func mpvCommand(args ...any) (map[string]any, error) {
	conn, err := net.DialTimeout("unix", mpvSocket, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(1 * time.Second))

	payload, err := json.Marshal(map[string]any{"command": args})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		var resp map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}
		if _, hasEvent := resp["event"]; hasEvent {
			continue
		}
		return resp, nil
	}
	return nil, scanner.Err()
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

func getLocalIP() string {
	conn, err := net.Dial("udp4", "239.255.255.250:1900")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func deviceDescriptionXML() string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>%s</friendlyName>
    <manufacturer>go-dlna</manufacturer>
    <modelName>mpv-renderer</modelName>
    <UDN>%s</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
        <controlURL>/ctrl/avt</controlURL>
        <eventSubURL>/evt/avt</eventSubURL>
        <SCPDURL>/scpd/avt</SCPDURL>
      </service>
      <service>
        <serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:RenderingControl</serviceId>
        <controlURL>/ctrl/rc</controlURL>
        <eventSubURL>/evt/rc</eventSubURL>
        <SCPDURL>/scpd/rc</SCPDURL>
      </service>
      <service>
        <serviceType>urn:schemas-upnp-org:service:ConnectionManager:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:ConnectionManager</serviceId>
        <controlURL>/ctrl/cm</controlURL>
        <eventSubURL>/evt/cm</eventSubURL>
        <SCPDURL>/scpd/cm</SCPDURL>
      </service>
    </serviceList>
  </device>
</root>`, deviceName, deviceUUID)
}

func avtSCPDXML() string {
	return `<?xml version="1.0"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <actionList>
    <action><name>SetAVTransportURI</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>CurrentURI</name><direction>in</direction><relatedStateVariable>AVTransportURI</relatedStateVariable></argument>
        <argument><name>CurrentURIMetaData</name><direction>in</direction><relatedStateVariable>AVTransportURIMetaData</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>Play</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Speed</name><direction>in</direction><relatedStateVariable>TransportPlaySpeed</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>Stop</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>Pause</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>Seek</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Unit</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SeekMode</relatedStateVariable></argument>
        <argument><name>Target</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SeekTarget</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>GetTransportInfo</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>CurrentTransportState</name><direction>out</direction><relatedStateVariable>TransportState</relatedStateVariable></argument>
        <argument><name>CurrentTransportStatus</name><direction>out</direction><relatedStateVariable>TransportStatus</relatedStateVariable></argument>
        <argument><name>CurrentSpeed</name><direction>out</direction><relatedStateVariable>TransportPlaySpeed</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>GetPositionInfo</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>Track</name><direction>out</direction><relatedStateVariable>CurrentTrack</relatedStateVariable></argument>
        <argument><name>TrackDuration</name><direction>out</direction><relatedStateVariable>CurrentTrackDuration</relatedStateVariable></argument>
        <argument><name>TrackMetaData</name><direction>out</direction><relatedStateVariable>CurrentTrackMetaData</relatedStateVariable></argument>
        <argument><name>TrackURI</name><direction>out</direction><relatedStateVariable>CurrentTrackURI</relatedStateVariable></argument>
        <argument><name>RelTime</name><direction>out</direction><relatedStateVariable>RelativeTimePosition</relatedStateVariable></argument>
        <argument><name>AbsTime</name><direction>out</direction><relatedStateVariable>AbsoluteTimePosition</relatedStateVariable></argument>
        <argument><name>RelCount</name><direction>out</direction><relatedStateVariable>RelativeCounterPosition</relatedStateVariable></argument>
        <argument><name>AbsCount</name><direction>out</direction><relatedStateVariable>AbsoluteCounterPosition</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action><name>GetMediaInfo</name>
      <argumentList>
        <argument><name>InstanceID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_InstanceID</relatedStateVariable></argument>
        <argument><name>NrTracks</name><direction>out</direction><relatedStateVariable>NumberOfTracks</relatedStateVariable></argument>
        <argument><name>MediaDuration</name><direction>out</direction><relatedStateVariable>CurrentMediaDuration</relatedStateVariable></argument>
        <argument><name>CurrentURI</name><direction>out</direction><relatedStateVariable>AVTransportURI</relatedStateVariable></argument>
        <argument><name>CurrentURIMetaData</name><direction>out</direction><relatedStateVariable>AVTransportURIMetaData</relatedStateVariable></argument>
        <argument><name>NextURI</name><direction>out</direction><relatedStateVariable>NextAVTransportURI</relatedStateVariable></argument>
        <argument><name>NextURIMetaData</name><direction>out</direction><relatedStateVariable>NextAVTransportURIMetaData</relatedStateVariable></argument>
        <argument><name>PlayMedium</name><direction>out</direction><relatedStateVariable>PlaybackStorageMedium</relatedStateVariable></argument>
        <argument><name>RecordMedium</name><direction>out</direction><relatedStateVariable>RecordStorageMedium</relatedStateVariable></argument>
        <argument><name>WriteStatus</name><direction>out</direction><relatedStateVariable>RecordMediumWriteStatus</relatedStateVariable></argument>
      </argumentList>
    </action>
  </actionList>
  <serviceStateTable>
    <stateVariable><name>TransportState</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>TransportStatus</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>TransportPlaySpeed</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>AVTransportURI</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>AVTransportURIMetaData</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>NextAVTransportURI</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>NextAVTransportURIMetaData</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>CurrentTrack</name><dataType>ui4</dataType></stateVariable>
    <stateVariable><name>CurrentTrackDuration</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>CurrentTrackMetaData</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>CurrentTrackURI</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>RelativeTimePosition</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>AbsoluteTimePosition</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>RelativeCounterPosition</name><dataType>i4</dataType></stateVariable>
    <stateVariable><name>AbsoluteCounterPosition</name><dataType>i4</dataType></stateVariable>
    <stateVariable><name>CurrentMediaDuration</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>NumberOfTracks</name><dataType>ui4</dataType></stateVariable>
    <stateVariable><name>PlaybackStorageMedium</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>RecordStorageMedium</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>RecordMediumWriteStatus</name><dataType>string</dataType></stateVariable>
    <stateVariable><name>A_ARG_TYPE_InstanceID</name><dataType>ui4</dataType></stateVariable>
    <stateVariable><name>A_ARG_TYPE_SeekMode</name><dataType>string</dataType><allowedValueList><allowedValue>REL_TIME</allowedValue><allowedValue>ABS_TIME</allowedValue></allowedValueList></stateVariable>
    <stateVariable><name>A_ARG_TYPE_SeekTarget</name><dataType>string</dataType></stateVariable>
  </serviceStateTable>
</scpd>`
}

func genericSCPDXML() string {
	return `<?xml version="1.0"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <actionList></actionList>
  <serviceStateTable></serviceStateTable>
</scpd>`
}

func soapOK(action string) string {
	parts := strings.SplitN(action, "#", 2)
	if len(parts) != 2 {
		return soapResponse("u", "Response", "urn:schemas-upnp-org:service:AVTransport:1", "")
	}
	serviceType := parts[0]
	actionName := parts[1]
	return soapResponse("u", actionName+"Response", serviceType, "")
}

func soapResponse(prefix, action, xmlns, body string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<%s:%s xmlns:%s="%s">%s</%s:%s>
</s:Body>
</s:Envelope>`, prefix, action, prefix, xmlns, body, prefix, action)
}

func extractTag(body, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	s := strings.Index(body, open)
	if s == -1 {
		open = "<" + tag + " "
		s = strings.Index(body, open)
		if s == -1 {
			return ""
		}
		e := strings.Index(body[s:], ">")
		if e == -1 {
			return ""
		}
		s = s + e + 1
	} else {
		s += len(open)
	}
	e := strings.Index(body[s:], close)
	if e == -1 {
		return ""
	}
	return body[s : s+e]
}

func playURL(url, subURL string) {
	mpvMu.Lock()

	if url == "" {
		mpvMu.Unlock()
		return
	}

	if mpvCmd != nil && mpvCmd.Process != nil && playingURL == url {
		start := pendingStart
		pendingStart = ""
		mpvMu.Unlock()
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
		return
	}

	if mpvCmd != nil && mpvCmd.Process != nil {
		mpvCmd.Process.Kill()
		mpvCmd = nil
	}

	os.Remove(mpvSocket)

	currentURL = url
	playingURL = url
	fmt.Printf("\n▶ playing: %s\n", url)
	if subURL != "" {
		fmt.Printf("   sub    : %s\n", subURL)
	}
	args := []string{url, "--input-ipc-server=" + mpvSocket}
	if subURL != "" {
		args = append(args, "--sub-file="+subURL)
	}
	if pendingStart != "" {
		args = append(args, "--start="+pendingStart)
		fmt.Printf("   start  : %s\n", pendingStart)
		pendingStart = ""
	}
	cmd := exec.Command("mpv", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Println("mpv error:", err)
		playingURL = ""
		mpvMu.Unlock()
		return
	}
	mpvCmd = cmd
	mpvMu.Unlock()
	go func() {
		cmd.Wait()
		mpvMu.Lock()
		if mpvCmd == cmd {
			mpvCmd = nil
			playingURL = ""
		}
		mpvMu.Unlock()
	}()
}

func stopPlayback() {
	mpvMu.Lock()
	defer mpvMu.Unlock()
	if mpvCmd != nil && mpvCmd.Process != nil {
		mpvCmd.Process.Kill()
		mpvCmd = nil
	}
	currentURL = ""
	currentSub = ""
	pendingStart = ""
	playingURL = ""
}

func extractSubtitleURL(metadata string, headers http.Header) string {
	if h := headers.Get("CaptionInfo.sec"); h != "" {
		return h
	}
	if metadata == "" {
		return ""
	}
	meta := html.UnescapeString(metadata)
	if strings.Contains(meta, "&lt;") || strings.Contains(meta, "&amp;") {
		meta = html.UnescapeString(meta)
	}
	for _, tag := range []string{"sec:CaptionInfoEx", "sec:CaptionInfo", "pv:subtitleFileUri"} {
		if u := strings.TrimSpace(extractTag(meta, tag)); u != "" {
			return u
		}
	}
	lower := strings.ToLower(meta)
	subMimes := []string{"text/srt", "text/vtt", "application/x-subrip", "smi/caption", "text/sub", "application/ttml", "text/x-ssa"}
	idx := 0
	for {
		i := strings.Index(lower[idx:], "<res ")
		if i == -1 {
			break
		}
		i += idx
		j := strings.Index(lower[i:], "</res>")
		if j == -1 {
			break
		}
		j += i
		seg := meta[i:j]
		segLower := lower[i:j]
		for _, m := range subMimes {
			if strings.Contains(segLower, m) {
				if gt := strings.Index(seg, ">"); gt != -1 {
					return strings.TrimSpace(seg[gt+1:])
				}
			}
		}
		idx = j + len("</res>")
	}
	return ""
}

func handleControl(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	soapAction := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	bodyStr := string(body)

	var xmlns, actionName string
	if parts := strings.SplitN(soapAction, "#", 2); len(parts) == 2 {
		xmlns, actionName = parts[0], parts[1]
	}

	log.Printf("SOAP action: %s", soapAction)

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")

	switch actionName {
	case "SetAVTransportURI":
		uri := html.UnescapeString(extractTag(bodyStr, "CurrentURI"))
		meta := extractTag(bodyStr, "CurrentURIMetaData")
		sub := extractSubtitleURL(meta, r.Header)
		mpvMu.Lock()
		currentURL = uri
		currentSub = sub
		mpvMu.Unlock()
		log.Printf("SetAVTransportURI: %s (sub=%s)", uri, sub)
		io.WriteString(w, soapOK(soapAction))

	case "Play":
		mpvMu.Lock()
		url, sub := currentURL, currentSub
		mpvMu.Unlock()
		log.Printf("Play: %s (sub=%s)", url, sub)
		go playURL(url, sub)
		io.WriteString(w, soapOK(soapAction))

	case "Stop":
		log.Println("Stop")
		go stopPlayback()
		io.WriteString(w, soapOK(soapAction))

	case "Pause":
		log.Println("Pause")
		if _, err := mpvCommand("set_property", "pause", true); err != nil {
			log.Printf("mpv pause failed: %v", err)
		}
		io.WriteString(w, soapOK(soapAction))

	case "Seek":
		unit := extractTag(bodyStr, "Unit")
		target := extractTag(bodyStr, "Target")
		log.Printf("Seek: unit=%s target=%s", unit, target)
		if unit == "REL_TIME" || unit == "ABS_TIME" {
			if secs, err := parseHMS(target); err == nil {
				if mpvRunning() {
					if resp, err := mpvCommand("seek", secs, "absolute"); err != nil {
						log.Printf("mpv seek ipc error: %v — queuing as pending start", err)
						mpvMu.Lock()
						pendingStart = target
						mpvMu.Unlock()
					} else {
						log.Printf("mpv seek → %s (%.1fs): %v", target, secs, resp["error"])
						mpvMu.Lock()
						pendingStart = target
						mpvMu.Unlock()
					}
				} else {
					mpvMu.Lock()
					pendingStart = target
					mpvMu.Unlock()
				}
			} else {
				log.Printf("seek parse error: %v", err)
			}
		}
		io.WriteString(w, soapOK(soapAction))

	case "GetTransportInfo":
		state := "STOPPED"
		if mpvRunning() {
			state = "PLAYING"
			if v, ok := mpvGet("pause"); ok {
				if paused, _ := v.(bool); paused {
					state = "PAUSED_PLAYBACK"
				}
			}
		}
		respBody := fmt.Sprintf(`<CurrentTransportState>%s</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus><CurrentSpeed>1</CurrentSpeed>`, state)
		io.WriteString(w, soapResponse("u", "GetTransportInfoResponse", xmlns, respBody))

	case "GetPositionInfo":
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
		mpvMu.Lock()
		trackURI := currentURL
		mpvMu.Unlock()
		respBody := fmt.Sprintf(`<Track>1</Track><TrackDuration>%s</TrackDuration><TrackMetaData></TrackMetaData><TrackURI>%s</TrackURI><RelTime>%s</RelTime><AbsTime>%s</AbsTime><RelCount>0</RelCount><AbsCount>0</AbsCount>`, dur, html.EscapeString(trackURI), pos, pos)
		io.WriteString(w, soapResponse("u", "GetPositionInfoResponse", xmlns, respBody))

	case "GetMediaInfo":
		dur := "00:00:00"
		if mpvRunning() {
			if v, ok := mpvGet("duration"); ok {
				if f, ok := v.(float64); ok {
					dur = formatHMS(f)
				}
			}
		}
		mpvMu.Lock()
		cur := currentURL
		mpvMu.Unlock()
		respBody := fmt.Sprintf(`<NrTracks>1</NrTracks><MediaDuration>%s</MediaDuration><CurrentURI>%s</CurrentURI><CurrentURIMetaData></CurrentURIMetaData><NextURI></NextURI><NextURIMetaData></NextURIMetaData><PlayMedium>NONE</PlayMedium><RecordMedium>NOT_IMPLEMENTED</RecordMedium><WriteStatus>NOT_IMPLEMENTED</WriteStatus>`, dur, html.EscapeString(cur))
		io.WriteString(w, soapResponse("u", "GetMediaInfoResponse", xmlns, respBody))

	default:
		log.Printf("unhandled SOAP action: %s\nheaders: %v\nbody: %s", soapAction, r.Header, bodyStr)
		io.WriteString(w, soapOK(soapAction))
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

func handleSubscribe(w http.ResponseWriter, r *http.Request) {
	sid := fmt.Sprintf("uuid:sub-%d", time.Now().UnixNano())
	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", "Second-1800")
	w.WriteHeader(200)
}

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
	mux.HandleFunc("/scpd/rc", xmlHandler(genericSCPDXML))
	mux.HandleFunc("/scpd/cm", xmlHandler(genericSCPDXML))

	mux.HandleFunc("/ctrl/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			handleControl(w, r)
		} else if r.Method == "SUBSCRIBE" || r.Method == "UNSUBSCRIBE" {
			handleSubscribe(w, r)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/evt/", func(w http.ResponseWriter, r *http.Request) {
		handleSubscribe(w, r)
	})

	mux.HandleFunc("/sub", handleSubAPI)

	srv := &http.Server{
		Addr:              ":" + httpPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("HTTP server on %s:%s", localIP, httpPort)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal("HTTP:", err)
	}
}

func ssdpNotify(conn *net.UDPConn, addr *net.UDPAddr, nts string) {
	location := fmt.Sprintf("http://%s:%s/description.xml", localIP, httpPort)
	types := []string{
		"upnp:rootdevice",
		deviceUUID,
		"urn:schemas-upnp-org:device:MediaRenderer:1",
		"urn:schemas-upnp-org:service:AVTransport:1",
		"urn:schemas-upnp-org:service:RenderingControl:1",
		"urn:schemas-upnp-org:service:ConnectionManager:1",
	}
	usns := []string{
		deviceUUID + "::upnp:rootdevice",
		deviceUUID,
		deviceUUID + "::urn:schemas-upnp-org:device:MediaRenderer:1",
		deviceUUID + "::urn:schemas-upnp-org:service:AVTransport:1",
		deviceUUID + "::urn:schemas-upnp-org:service:RenderingControl:1",
		deviceUUID + "::urn:schemas-upnp-org:service:ConnectionManager:1",
	}
	for i, nt := range types {
		msg := fmt.Sprintf("NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: %s\r\nNT: %s\r\nNTS: %s\r\nSERVER: Linux/1.0 UPnP/1.0 go-dlna/1.0\r\nUSN: %s\r\n\r\n",
			location, nt, nts, usns[i])
		conn.WriteToUDP([]byte(msg), addr)
	}
}

func respondMSearch(conn *net.UDPConn, addr *net.UDPAddr, st string) {
	location := fmt.Sprintf("http://%s:%s/description.xml", localIP, httpPort)

	respond := func(nt, usn string) {
		msg := fmt.Sprintf("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: %s\r\nSERVER: Linux/1.0 UPnP/1.0 go-dlna/1.0\r\nST: %s\r\nUSN: %s\r\n\r\n",
			location, nt, usn)
		conn.WriteToUDP([]byte(msg), addr)
	}

	switch {
	case st == "ssdp:all" || st == "upnp:rootdevice":
		respond("upnp:rootdevice", deviceUUID+"::upnp:rootdevice")
		respond("urn:schemas-upnp-org:device:MediaRenderer:1", deviceUUID+"::urn:schemas-upnp-org:device:MediaRenderer:1")
	case strings.Contains(st, "MediaRenderer"):
		respond(st, deviceUUID+"::"+st)
	case strings.Contains(st, "AVTransport") || strings.Contains(st, "RenderingControl") || strings.Contains(st, "ConnectionManager"):
		respond(st, deviceUUID+"::"+st)
	case st == deviceUUID:
		respond(deviceUUID, deviceUUID)
	}
}

func startSSDP() {
	mcastAddr, _ := net.ResolveUDPAddr("udp4", ssdpMulticast)

	outConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(localIP), Port: 0})
	if err != nil {
		log.Printf("SSDP out bind to %s failed (%v), falling back to any", localIP, err)
		outConn, err = net.ListenUDP("udp4", nil)
		if err != nil {
			log.Fatal("SSDP out:", err)
		}
	}

	listenAddr, _ := net.ResolveUDPAddr("udp4", "0.0.0.0:1900")
	listenConn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		log.Printf("SSDP listen on :1900 failed (%v), trying random port", err)
		listenConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(localIP)})
		if err != nil {
			log.Fatal("SSDP:", err)
		}
	}

	joinMulticast(listenConn, mcastAddr)

	ssdpNotify(outConn, mcastAddr, "ssdp:alive")
	log.Println("SSDP alive sent")

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			ssdpNotify(outConn, mcastAddr, "ssdp:alive")
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, addr, err := listenConn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(msg, "M-SEARCH") {
			continue
		}
		st := extractHeader(msg, "ST")
		if st == "" {
			continue
		}
		log.Printf("M-SEARCH from %s ST=%s", addr, st)
		go respondMSearch(outConn, addr, st)
	}
}

func extractHeader(response, header string) string {
	for line := range strings.SplitSeq(response, "\r\n") {
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(header)+":") {
			return strings.TrimSpace(line[len(header)+1:])
		}
	}
	return ""
}

func byebye() {
	mcastAddr, _ := net.ResolveUDPAddr("udp4", ssdpMulticast)
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return
	}
	defer conn.Close()
	msg := "NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nNT: urn:schemas-upnp-org:device:MediaRenderer:1\r\nNTS: ssdp:byebye\r\nUSN: " + deviceUUID + "::urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"
	conn.WriteToUDP([]byte(msg), mcastAddr)
}

func joinMulticast(conn *net.UDPConn, mcastAddr *net.UDPAddr) {
	ip4 := mcastAddr.IP.To4()
	if ip4 == nil {
		return
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	fd, err := conn.File()
	if err != nil {
		return
	}
	defer fd.Close()
	sockFd := int(fd.Fd())

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		mreq := &syscall.IPMreq{}
		copy(mreq.Multiaddr[:], ip4)
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP.To4()
			case *net.IPAddr:
				ip = v.IP.To4()
			}
			if ip == nil {
				continue
			}
			copy(mreq.Interface[:], ip)
			syscall.SetsockoptIPMreq(sockFd, syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq)
		}
	}
}

func main() {
	localIP = getLocalIP()
	fmt.Printf("dlna-renderer starting\n")
	fmt.Printf("  device name : %s\n", deviceName)
	fmt.Printf("  local IP    : %s\n", localIP)
	fmt.Printf("  HTTP port   : %s\n", httpPort)
	fmt.Printf("  description : http://%s:%s/description.xml\n\n", localIP, httpPort)
	fmt.Println("waiting for DLNA controller to connect...")

	go startHTTP()
	go startSSDP()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nshutting down...")
	byebye()
	stopPlayback()
}
