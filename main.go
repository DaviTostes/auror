package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ssdpMulticast = "239.255.255.250:1900"
	deviceUUID    = "uuid:11111111-2222-3333-4444-555555555555"
	deviceName    = "mpv-renderer"
)

var (
	httpPort   = "49152"
	localIP    string
	mpvCmd     *exec.Cmd
	mpvMu      sync.Mutex
	currentURL string
)

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

func playURL(url string) {
	mpvMu.Lock()
	defer mpvMu.Unlock()

	if mpvCmd != nil && mpvCmd.Process != nil {
		mpvCmd.Process.Kill()
		mpvCmd.Wait()
		mpvCmd = nil
	}

	if url == "" {
		return
	}

	currentURL = url
	fmt.Printf("\n▶ playing: %s\n", url)
	cmd := exec.Command("mpv", url)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Println("mpv error:", err)
		return
	}
	mpvCmd = cmd
	go func() {
		cmd.Wait()
		mpvMu.Lock()
		if mpvCmd == cmd {
			mpvCmd = nil
		}
		mpvMu.Unlock()
	}()
}

func stopPlayback() {
	mpvMu.Lock()
	defer mpvMu.Unlock()
	if mpvCmd != nil && mpvCmd.Process != nil {
		mpvCmd.Process.Kill()
		mpvCmd.Wait()
		mpvCmd = nil
	}
	currentURL = ""
}

func handleControl(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	soapAction := r.Header.Get("SOAPAction")
	soapAction = strings.Trim(soapAction, `"`)
	bodyStr := string(body)

	log.Printf("SOAP action: %s", soapAction)

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")

	switch {
	case strings.Contains(soapAction, "SetAVTransportURI"):
		uri := extractTag(bodyStr, "CurrentURI")
		uri = strings.ReplaceAll(uri, "&amp;", "&")
		uri = strings.ReplaceAll(uri, "&lt;", "<")
		uri = strings.ReplaceAll(uri, "&gt;", ">")
		currentURL = uri
		log.Printf("SetAVTransportURI: %s", uri)
		fmt.Fprintf(w, soapOK(soapAction))

	case strings.Contains(soapAction, "Play"):
		log.Printf("Play: %s", currentURL)
		go playURL(currentURL)
		fmt.Fprintf(w, soapOK(soapAction))

	case strings.Contains(soapAction, "Stop"):
		log.Println("Stop")
		go stopPlayback()
		fmt.Fprintf(w, soapOK(soapAction))

	case strings.Contains(soapAction, "Pause"):
		log.Println("Pause (not supported, sending OK)")
		fmt.Fprintf(w, soapOK(soapAction))

	case strings.Contains(soapAction, "GetTransportInfo"):
		state := "STOPPED"
		mpvMu.Lock()
		if mpvCmd != nil && mpvCmd.Process != nil {
			state = "PLAYING"
		}
		mpvMu.Unlock()
		body := fmt.Sprintf(`<CurrentTransportState>%s</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus><CurrentSpeed>1</CurrentSpeed>`, state)
		parts := strings.SplitN(soapAction, "#", 2)
		xmlns := parts[0]
		fmt.Fprintf(w, soapResponse("u", "GetTransportInfoResponse", xmlns, body))

	case strings.Contains(soapAction, "GetPositionInfo"):
		body := `<Track>1</Track><TrackDuration>00:00:00</TrackDuration><TrackMetaData></TrackMetaData><TrackURI></TrackURI><RelTime>00:00:00</RelTime><AbsTime>00:00:00</AbsTime><RelCount>0</RelCount><AbsCount>0</AbsCount>`
		parts := strings.SplitN(soapAction, "#", 2)
		xmlns := parts[0]
		fmt.Fprintf(w, soapResponse("u", "GetPositionInfoResponse", xmlns, body))

	case strings.Contains(soapAction, "GetMediaInfo"):
		body := `<NrTracks>1</NrTracks><MediaDuration>00:00:00</MediaDuration><CurrentURI></CurrentURI><CurrentURIMetaData></CurrentURIMetaData><NextURI></NextURI><NextURIMetaData></NextURIMetaData><PlayMedium>NONE</PlayMedium><RecordMedium>NOT_IMPLEMENTED</RecordMedium><WriteStatus>NOT_IMPLEMENTED</WriteStatus>`
		parts := strings.SplitN(soapAction, "#", 2)
		xmlns := parts[0]
		fmt.Fprintf(w, soapResponse("u", "GetMediaInfoResponse", xmlns, body))

	default:
		log.Printf("unhandled SOAP action: %s", soapAction)
		fmt.Fprintf(w, soapOK(soapAction))
	}
}

func handleSubscribe(w http.ResponseWriter, r *http.Request) {
	sid := fmt.Sprintf("uuid:sub-%d", time.Now().UnixNano())
	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", "Second-1800")
	w.WriteHeader(200)
}

func startHTTP() {
	mux := http.NewServeMux()

	mux.HandleFunc("/description.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		fmt.Fprint(w, deviceDescriptionXML())
	})

	mux.HandleFunc("/scpd/avt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		fmt.Fprint(w, avtSCPDXML())
	})

	mux.HandleFunc("/scpd/rc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		fmt.Fprint(w, genericSCPDXML())
	})

	mux.HandleFunc("/scpd/cm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		fmt.Fprint(w, genericSCPDXML())
	})

	mux.HandleFunc("/ctrl/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			handleControl(w, r)
		} else if r.Method == "SUBSCRIBE" || r.Method == "UNSUBSCRIBE" {
			handleSubscribe(w, r)
		} else {
			w.WriteHeader(405)
		}
	})

	mux.HandleFunc("/evt/", func(w http.ResponseWriter, r *http.Request) {
		handleSubscribe(w, r)
	})

	log.Printf("HTTP server on %s:%s", localIP, httpPort)
	if err := http.ListenAndServe(":"+httpPort, mux); err != nil {
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
		outConn, _ = net.ListenUDP("udp4", nil)
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
	for _, line := range strings.Split(response, "\r\n") {
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(header)+":") {
			return strings.TrimSpace(line[len(header)+1:])
		}
	}
	return ""
}

func getIfaceName() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.String() == localIP {
				return iface.Name
			}
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
	var buf bytes.Buffer
	buf.WriteString("NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nNT: urn:schemas-upnp-org:device:MediaRenderer:1\r\nNTS: ssdp:byebye\r\nUSN: " + deviceUUID + "::urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n")
	conn.WriteToUDP(buf.Bytes(), mcastAddr)
}

func joinMulticast(conn *net.UDPConn, mcastAddr *net.UDPAddr) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		ip4 := mcastAddr.IP.To4()
		if ip4 == nil {
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
			fd, err := conn.File()
			if err != nil {
				continue
			}
			syscall.SetsockoptIPMreq(int(fd.Fd()), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq)
			fd.Close()
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
