package main

import (
	"fmt"
	"log"
	mathrand "math/rand"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const ssdpMulticast = "239.255.255.250:1900"

func ssdpNotify(conn *net.UDPConn, addr *net.UDPAddr, nts string) {
	location := fmt.Sprintf("http://%s:%s/description.xml", currentLocalIP(), httpPort)
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
	location := fmt.Sprintf("http://%s:%s/description.xml", currentLocalIP(), httpPort)

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
	ip := currentLocalIP()

	outConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip), Port: 0})
	if err != nil {
		log.Printf("SSDP out bind to %s failed (%v), falling back to any", ip, err)
		outConn, err = net.ListenUDP("udp4", nil)
		if err != nil {
			log.Fatal("SSDP out:", err)
		}
	}

	listenAddr, _ := net.ResolveUDPAddr("udp4", "0.0.0.0:1900")
	listenConn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		log.Printf("SSDP listen on :1900 failed (%v), trying random port", err)
		listenConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(ip)})
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

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			if changed, old, neu := refreshLocalIP(); changed {
				log.Printf("local IP changed: %s → %s, re-announcing", old, neu)
				ssdpNotify(outConn, mcastAddr, "ssdp:byebye")
				ssdpNotify(outConn, mcastAddr, "ssdp:alive")
			}
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
		mxStr := strings.TrimSpace(extractHeader(msg, "MX"))
		mx, _ := strconv.Atoi(mxStr)
		if mx < 0 {
			mx = 0
		}
		if mx > 5 {
			mx = 5
		}
		log.Printf("M-SEARCH from %s ST=%s MX=%d", addr, st, mx)
		delay := time.Duration(0)
		if mx > 0 {
			delay = time.Duration(mathrand.Intn(mx*1000)) * time.Millisecond
		}
		go func(a *net.UDPAddr, s string, d time.Duration) {
			if d > 0 {
				time.Sleep(d)
			}
			respondMSearch(outConn, a, s)
		}(addr, st, delay)
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
