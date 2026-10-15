package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// newStreamClient builds the HTTP client used to relay streams. There is no
// overall timeout (streams run for hours), only connection-level ones.
func newStreamClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}
}

// handleStream relays an HTTPS radio stream as plain HTTP. vTuner receivers
// cannot do TLS and the stream of many modern stations is HTTPS-only, so the
// receiver connects here and this server talks TLS to the station.
//
// Only streams of known stations can be requested (by ID), never arbitrary
// URLs, so this is not an open proxy.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing station id", http.StatusBadRequest)
		return
	}
	st := s.lookupStation(r.Context(), id, true)
	if st == nil {
		http.Error(w, "station not found", http.StatusNotFound)
		return
	}
	lower := strings.ToLower(st.URL)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		s.log.Error("stream URL has an unsupported scheme", "station", id, "url", st.URL)
		http.Error(w, "unsupported stream URL", http.StatusBadGateway)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, st.URL, http.NoBody)
	if err != nil {
		http.Error(w, "bad stream URL", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "YCast-go/"+s.cfg.Version)
	if v := r.Header.Get("Icy-MetaData"); v != "" {
		req.Header.Set("Icy-MetaData", v)
	}
	resp, err := s.stream.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Error("stream connection failed", "station", id, "err", err)
		}
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.log.Error("stream returned an error", "station", id, "status", resp.StatusCode)
		http.Error(w, fmt.Sprintf("upstream status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	h := w.Header()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}
	for k, vals := range resp.Header {
		if strings.HasPrefix(k, "Icy-") {
			h[k] = vals
		}
	}
	h.Set("Cache-Control", "no-cache, no-store")
	// Live stream of unknown length: ask net/http not to use chunked encoding
	// (some embedded players choke on it) and to close the connection at the end.
	h.Set("Transfer-Encoding", "identity")
	w.WriteHeader(http.StatusOK)

	s.log.Info("relaying stream", "station", id, "name", st.Name)
	rc := http.NewResponseController(w)
	buf := make([]byte, 16<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // receiver went away
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
		if rerr != nil {
			if rerr != io.EOF && r.Context().Err() == nil {
				s.log.Warn("stream interrupted", "station", id, "err", rerr)
			}
			return
		}
	}
}
