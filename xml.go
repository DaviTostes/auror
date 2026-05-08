package main

import (
	"encoding/xml"
	"html"
	"net/http"
	"strings"
)

// xmlExtract walks the XML, returns a map from local name → text content for
// all listed names. Robust against namespaces, attributes and CDATA.
func xmlExtract(body string, names ...string) map[string]string {
	out := map[string]string{}
	targets := map[string]struct{}{}
	for _, n := range names {
		targets[n] = struct{}{}
	}
	dec := xml.NewDecoder(strings.NewReader(body))
	dec.Strict = false
	var current string
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if _, ok := targets[t.Name.Local]; ok && current == "" {
				current = t.Name.Local
				buf.Reset()
			}
		case xml.CharData:
			if current != "" {
				buf.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == current {
				if _, exists := out[current]; !exists {
					out[current] = buf.String()
				}
				current = ""
				buf.Reset()
			}
		}
	}
	return out
}

func extractTag(body, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
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
	e := strings.Index(body[s:], closeTag)
	if e == -1 {
		return ""
	}
	return body[s : s+e]
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
