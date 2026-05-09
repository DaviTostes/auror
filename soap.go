package main

import (
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

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

// UPnP fault codes — partial; see UPnP Device Architecture v1.0 §3.2.2.
const (
	upnpErrInvalidAction   = 401
	upnpErrInvalidArgs     = 402
	upnpErrActionFailed    = 501
	upnpErrIllegalSeekMode = 710
	upnpErrIllegalSeekTgt  = 711
)

func soapFault(code int, desc string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<s:Fault>
<faultcode>s:Client</faultcode>
<faultstring>UPnPError</faultstring>
<detail>
<UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
<errorCode>%d</errorCode>
<errorDescription>%s</errorDescription>
</UPnPError>
</detail>
</s:Fault>
</s:Body>
</s:Envelope>`, code, html.EscapeString(desc))
}

func writeFault(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	io.WriteString(w, soapFault(code, desc))
}

func handleControl(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeFault(w, upnpErrActionFailed, "read body: "+err.Error())
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

	tags := xmlExtract(bodyStr,
		"CurrentURI", "CurrentURIMetaData",
		"NextURI", "NextURIMetaData",
		"Unit", "Target",
		"DesiredVolume", "DesiredMute",
		"Channel", "InstanceID",
	)

	switch actionName {
	case "SetAVTransportURI":
		uri := tags["CurrentURI"]
		meta := tags["CurrentURIMetaData"]
		sub := extractSubtitleURL(meta, r.Header)
		mpvMu.Lock()
		currentURL = uri
		currentSub = sub
		mpvMu.Unlock()
		log.Printf("SetAVTransportURI: %s (sub=%s)", uri, sub)
		setTransportState("STOPPED")
		fireEvent("avt")
		io.WriteString(w, soapOK(soapAction))

	case "SetNextAVTransportURI":
		uri := tags["NextURI"]
		meta := tags["NextURIMetaData"]
		sub := extractSubtitleURL(meta, r.Header)
		mpvMu.Lock()
		nextURL = uri
		nextSub = sub
		running := mpvCmd != nil && mpvCmd.Process != nil
		mpvMu.Unlock()
		log.Printf("SetNextAVTransportURI: %s (sub=%s)", uri, sub)
		if running && uri != "" {
			if _, err := mpvCommand("playlist-clear"); err != nil {
				log.Printf("playlist-clear failed: %v", err)
			}
			if _, err := mpvCommand("loadfile", uri, "append-play"); err != nil {
				log.Printf("loadfile append-play failed: %v", err)
			}
		}
		fireEvent("avt")
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
		if mpvRunning() {
			if _, err := mpvCommand("stop"); err != nil {
				log.Printf("mpv stop failed: %v — falling back to kill", err)
				go stopPlayback()
			} else {
				mpvMu.Lock()
				currentURL = ""
				currentSub = ""
				pendingStart = ""
				playingURL = ""
				nextURL = ""
				nextSub = ""
				mpvMu.Unlock()
				setTransportState("STOPPED")
			}
		} else {
			setTransportState("STOPPED")
		}
		io.WriteString(w, soapOK(soapAction))

	case "Pause":
		log.Println("Pause")
		if _, err := mpvCommand("set_property", "pause", true); err != nil {
			log.Printf("mpv pause failed: %v", err)
		}
		setTransportState("PAUSED_PLAYBACK")
		io.WriteString(w, soapOK(soapAction))

	case "Seek":
		unit := tags["Unit"]
		target := tags["Target"]
		log.Printf("Seek: unit=%s target=%s", unit, target)
		if unit != "REL_TIME" && unit != "ABS_TIME" {
			writeFault(w, upnpErrIllegalSeekMode, "unit must be REL_TIME or ABS_TIME")
			return
		}
		secs, err := parseHMS(target)
		if err != nil {
			writeFault(w, upnpErrIllegalSeekTgt, "bad target: "+err.Error())
			return
		}
		queue := true
		if mpvRunning() {
			if resp, err := mpvCommand("seek", secs, "absolute"); err != nil {
				log.Printf("mpv seek ipc error: %v — queuing as pending start", err)
			} else {
				log.Printf("mpv seek → %s (%.1fs): %v", target, secs, resp["error"])
				queue = false
			}
		}
		if queue {
			mpvMu.Lock()
			pendingStart = target
			mpvMu.Unlock()
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

	case "Next":
		log.Println("Next")
		if mpvRunning() {
			if _, err := mpvCommand("playlist-next", "force"); err != nil {
				log.Printf("playlist-next failed: %v", err)
			}
		}
		io.WriteString(w, soapOK(soapAction))

	case "Previous":
		log.Println("Previous")
		if mpvRunning() {
			if _, err := mpvCommand("playlist-prev", "force"); err != nil {
				log.Printf("playlist-prev failed: %v", err)
			}
		}
		io.WriteString(w, soapOK(soapAction))

	case "GetVolume":
		vol := 100
		if mpvRunning() {
			if v, ok := mpvGet("volume"); ok {
				if f, ok := v.(float64); ok {
					vol = int(f)
				}
			}
		}
		respBody := fmt.Sprintf(`<CurrentVolume>%d</CurrentVolume>`, vol)
		io.WriteString(w, soapResponse("u", "GetVolumeResponse", xmlns, respBody))

	case "SetVolume":
		desired := strings.TrimSpace(tags["DesiredVolume"])
		log.Printf("SetVolume: %s", desired)
		v, err := strconv.Atoi(desired)
		if err != nil {
			writeFault(w, upnpErrInvalidArgs, "DesiredVolume must be integer")
			return
		}
		if v < 0 {
			v = 0
		} else if v > 100 {
			v = 100
		}
		if mpvRunning() {
			if _, err := mpvCommand("set_property", "volume", v); err != nil {
				log.Printf("mpv volume failed: %v", err)
			}
		}
		fireEvent("rc")
		io.WriteString(w, soapOK(soapAction))

	case "GetMute":
		mute := 0
		if mpvRunning() {
			if v, ok := mpvGet("mute"); ok {
				if b, ok := v.(bool); ok && b {
					mute = 1
				}
			}
		}
		respBody := fmt.Sprintf(`<CurrentMute>%d</CurrentMute>`, mute)
		io.WriteString(w, soapResponse("u", "GetMuteResponse", xmlns, respBody))

	case "SetMute":
		desired := strings.TrimSpace(tags["DesiredMute"])
		log.Printf("SetMute: %s", desired)
		var mute bool
		switch strings.ToLower(desired) {
		case "1", "true", "yes":
			mute = true
		case "0", "false", "no", "":
			mute = false
		default:
			writeFault(w, upnpErrInvalidArgs, "DesiredMute must be boolean")
			return
		}
		if mpvRunning() {
			if _, err := mpvCommand("set_property", "mute", mute); err != nil {
				log.Printf("mpv mute failed: %v", err)
			}
		}
		fireEvent("rc")
		io.WriteString(w, soapOK(soapAction))

	case "ListPresets":
		respBody := `<CurrentPresetNameList>FactoryDefaults</CurrentPresetNameList>`
		io.WriteString(w, soapResponse("u", "ListPresetsResponse", xmlns, respBody))

	case "GetProtocolInfo":
		respBody := fmt.Sprintf(`<Source></Source><Sink>%s</Sink>`, sinkProtocolInfo)
		io.WriteString(w, soapResponse("u", "GetProtocolInfoResponse", xmlns, respBody))

	case "GetCurrentConnectionIDs":
		io.WriteString(w, soapResponse("u", "GetCurrentConnectionIDsResponse", xmlns, `<ConnectionIDs>0</ConnectionIDs>`))

	case "GetCurrentConnectionInfo":
		respBody := `<RcsID>0</RcsID><AVTransportID>0</AVTransportID><ProtocolInfo></ProtocolInfo><PeerConnectionManager></PeerConnectionManager><PeerConnectionID>-1</PeerConnectionID><Direction>Input</Direction><Status>OK</Status>`
		io.WriteString(w, soapResponse("u", "GetCurrentConnectionInfoResponse", xmlns, respBody))

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
		nxt := nextURL
		mpvMu.Unlock()
		respBody := fmt.Sprintf(`<NrTracks>1</NrTracks><MediaDuration>%s</MediaDuration><CurrentURI>%s</CurrentURI><CurrentURIMetaData></CurrentURIMetaData><NextURI>%s</NextURI><NextURIMetaData></NextURIMetaData><PlayMedium>NONE</PlayMedium><RecordMedium>NOT_IMPLEMENTED</RecordMedium><WriteStatus>NOT_IMPLEMENTED</WriteStatus>`, dur, html.EscapeString(cur), html.EscapeString(nxt))
		io.WriteString(w, soapResponse("u", "GetMediaInfoResponse", xmlns, respBody))

	default:
		log.Printf("unhandled SOAP action: %s\nheaders: %v\nbody: %s", soapAction, r.Header, bodyStr)
		io.WriteString(w, soapOK(soapAction))
	}
}
