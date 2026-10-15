package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hgomez/ycast-go/internal/station"
	"github.com/hgomez/ycast-go/internal/vtuner"
)

// ------------------------------------------------------------------ helpers

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func get(t *testing.T, h http.Handler, host, uri string) (status int, hdr http.Header, body string) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+uri, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, string(data)
}

const testHost = "denon.vtuner.com"

// fakeRB is a minimal Radio Browser API.
type fakeRB struct {
	srv      *httptest.Server
	urlHits  atomic.Int32
	stations []map[string]any
	resolve  map[string]string // uuid -> URL returned by /json/url/<uuid>
}

func newFakeRB(t *testing.T) *fakeRB {
	t.Helper()
	f := &fakeRB{resolve: map[string]string{}}
	st := func(uuid, name, u, tags string, votes, ok int) map[string]any {
		return map[string]any{
			"stationuuid": uuid, "name": name, "url": u, "url_resolved": u, "favicon": "",
			"tags": tags, "countrycode": "FR", "language": "french", "votes": votes,
			"codec": "MP3", "bitrate": 128, "lastcheckok": ok,
		}
	}
	f.stations = []map[string]any{
		st("uuid-b", "b jazz radio", "https://b.example/live", "jazz,smooth", 30, 1),
		st("uuid-a", "A Jazz FM", "http://a.example/live", "jazz", 10, 1),
		st("uuid-c", "Broken Jazz", "http://c.example/live", "jazz", 99, 0),
		st("uuid-s", "Stream FM", "https://s.example/live", "talk", 5, 1),
	}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		// The fakes only ever encode JSON-safe values; ignore the write error
		// like errchkjson would allow, but keep the call checked for it.
		if err := json.NewEncoder(w).Encode(v); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
	mux.HandleFunc("/json/tags", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"name": "jazz", "stationcount": 50}, {"name": "rare", "stationcount": 3}, {"name": "rock", "stationcount": 900},
		})
	})
	mux.HandleFunc("/json/countries", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"name": "France", "stationcount": 300}})
	})
	mux.HandleFunc("/json/languages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"name": "french", "stationcount": 120}, {"name": "swiss german", "stationcount": 10}})
	})
	mux.HandleFunc("/json/stations", func(w http.ResponseWriter, r *http.Request) { // popular
		writeJSON(w, f.stations[:3])
	})
	mux.HandleFunc("/json/stations/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var out []map[string]any
		for _, s := range f.stations {
			if tag := q.Get("tag"); tag != "" && !strings.Contains(s["tags"].(string), tag) {
				continue
			}
			if name := q.Get("name"); name != "" && !strings.Contains(strings.ToLower(s["name"].(string)), strings.ToLower(name)) {
				continue
			}
			out = append(out, s)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/json/stations/byuuid/{uuid}", func(w http.ResponseWriter, r *http.Request) {
		for _, s := range f.stations {
			if s["stationuuid"] == r.PathValue("uuid") {
				writeJSON(w, []map[string]any{s})
				return
			}
		}
		writeJSON(w, []map[string]any{})
	})
	mux.HandleFunc("/json/url/{uuid}", func(w http.ResponseWriter, r *http.Request) {
		f.urlHits.Add(1)
		uuid := r.PathValue("uuid")
		u := f.resolve[uuid]
		if u == "" {
			for _, s := range f.stations {
				if s["stationuuid"] == uuid {
					u = s["url"].(string)
				}
			}
		}
		writeJSON(w, map[string]any{"ok": true, "stationuuid": uuid, "url": u})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newTestServer(t *testing.T, mod func(*Config)) (*Server, *fakeRB) {
	t.Helper()
	f := newFakeRB(t)
	cfg := Config{
		Listen:      "127.0.0.1",
		ProxyHTTPS:  true,
		MaxStations: 500,
		CacheDir:    filepath.Join(t.TempDir(), "icons"),
		RBAPI:       f.srv.URL,
		Version:     "test",
	}
	if mod != nil {
		mod(&cfg)
	}
	return New(cfg, discardLogger()), f
}

func writeStationsFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stations.yml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ------------------------------------------------------------------ paging

func TestPaging(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		query string
		want  []int
	}{
		{"", items},
		{"startitems=3&enditems=5", []int{3, 4, 5}},
		{"startItems=9&endItems=100", []int{9, 10}},
		{"start=4&howmany=3", []int{4, 5, 6}},
		{"startitems=1&enditems=100", items},
		{"startitems=11&enditems=20", nil},
		{"startitems=50", nil},
		{"startitems=5&enditems=2", nil},
		{"startitems=0&enditems=2", []int{1, 2}},
		{"startitems=abc&enditems=xyz", items},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		got := paged(items, q, discardLogger())
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("paged(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

// ---------------------------------------- byte-for-byte match with Python

func TestStationItemXMLMatchesPython(t *testing.T) {
	// Golden string produced by the original vtuner.py.
	const goldenStation = `<?xml version="1.0" encoding="UTF-8" standalone="yes" ?><ListOfItems><ItemCount>1</ItemCount><Item><ItemType>Station</ItemType><StationId>RB_abc-123</StationId><StationName>Radio d'Alsace &amp; Co</StationName><StationUrl>http://stream.example/live</StationUrl><StationDesc>jazz, soul</StationDesc><Logo>http://denon.vtuner.com/ycast/icon?id=RB_abc-123</Logo><StationFormat>jazz</StationFormat><StationLocation>FR</StationLocation><StationBandWidth>128</StationBandWidth><StationMime>MP3</StationMime><Relia>3</Relia><Bookmark /></Item></ListOfItems>`

	s, _ := newTestServer(t, func(c *Config) { c.ProxyHTTPS = false })
	req := httptest.NewRequest("GET", "http://"+testHost+"/x", http.NoBody)
	st := &station.Station{
		ID: "RB_abc-123", Name: "Radio d'Alsace & Co", URL: "https://stream.example/live",
		Tags: []string{"jazz", "soul"}, CountryCode: "FR", Codec: "MP3", Bitrate: 128, HasBitrate: true,
	}
	sp := &vtuner.Page{Count: 1}
	sp.Add(s.stationItem(req, st))
	if got := sp.String(); got != goldenStation {
		t.Errorf("station XML differs:\n got: %s\nwant: %s", got, goldenStation)
	}
}

// --------------------------------------------------------------- handlers

func TestLanding(t *testing.T) {
	s, _ := newTestServer(t, nil)
	_, hdr, body := get(t, s.Routes(), testHost, "/ycast/")
	if !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("content type %q", hdr.Get("Content-Type"))
	}
	if !strings.Contains(body, "<Title>Radiobrowser</Title>") ||
		!strings.Contains(body, "http://denon.vtuner.com/ycast/radiobrowser/?vtuner=true") ||
		!strings.Contains(body, "'My Stations' feature not configured.") ||
		!strings.Contains(body, "<ItemCount>2</ItemCount>") {
		t.Errorf("unexpected landing: %s", body)
	}

	s2, _ := newTestServer(t, func(c *Config) { c.StationsFile = writeStationsFile(t, "Cat:\n  A: http://a.example\n") })
	_, _, body = get(t, s2.Routes(), testHost, "/")
	if !strings.Contains(body, "<Title>My Stations</Title>") || !strings.Contains(body, "<DirCount>1</DirCount>") {
		t.Errorf("unexpected landing with stations: %s", body)
	}
}

func TestUpstreamEntryPoints(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.StationsFile = writeStationsFile(t, "Cat:\n  A: http://a.example\n") })
	h := s.Routes()

	_, _, body := get(t, h, testHost, "/setupapp/denon/asp/BrowseXML/loginXML.asp?token=0")
	if body != "<EncryptedToken>0000000000000000</EncryptedToken>" {
		t.Errorf("token = %q", body)
	}
	_, _, body = get(t, h, testHost, "/setupapp/denon/asp/BrowseXML/loginXML.asp?sSearchtype=3")
	if !strings.Contains(body, "<Title>My Stations</Title>") {
		t.Errorf("loginXML: %s", body)
	}
	_, _, body = get(t, h, testHost, "/setupapp/denon/asp/BrowseXML/navXML.asp?gofile=&mac=x")
	if !strings.Contains(body, "<Title>Genres</Title>") || !strings.Contains(body, "<Title>Most Popular</Title>") {
		t.Errorf("navXML: %s", body)
	}
	_, _, body = get(t, h, testHost, "/setupapp/denon/asp/BrowseXML/FavXML.asp?mac=x")
	if !strings.Contains(body, "<Title>Cat</Title>") {
		t.Errorf("FavXML: %s", body)
	}
	_, _, body = get(t, h, testHost, "/setupapp/denon/asp/BrowseXML/statxml.asp?id=MY_"+station.MyChecksum("A", "http://a.example"))
	if !strings.Contains(body, "<StationName>A</StationName>") {
		t.Errorf("statxml: %s", body)
	}
	_, _, body = get(t, h, testHost, "/setupapp/denon/asp/anything.asp?search=cat")
	if !strings.Contains(body, "Search query too short") && !strings.Contains(body, "<ItemCount>") {
		t.Errorf("search entry point: %s", body)
	}
	if code, _, _ := get(t, h, testHost, "/setupapp/unknown.asp"); code != http.StatusNotFound {
		t.Errorf("unknown upstream query: status %d", code)
	}
	if code, _, _ := get(t, h, testHost, "/nope"); code != http.StatusNotFound {
		t.Errorf("unknown path: status %d", code)
	}
}

func TestRadioBrowserDirectories(t *testing.T) {
	s, _ := newTestServer(t, nil)
	h := s.Routes()

	_, _, body := get(t, h, testHost, "/ycast/radiobrowser/")
	for _, want := range []string{
		"<Title>Genres</Title>", "<DirCount>2</DirCount>", // jazz, rock ("rare" is hidden)
		"<Title>Countries</Title>", "<DirCount>1</DirCount>",
		"<Title>Languages</Title>",
		"<Title>Most Popular</Title>", "<DirCount>2</DirCount>", // third station is broken
		"/ycast/radiobrowser/genre/?vtuner=true",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("RB landing misses %q:\n%s", want, body)
		}
	}

	_, _, body = get(t, h, testHost, "/ycast/radiobrowser/genre/")
	if !strings.Contains(body, "<Title>Jazz</Title>") || !strings.Contains(body, "/genre/jazz?vtuner=true") ||
		strings.Contains(body, "rare") || !strings.Contains(body, "<DirCount>500</DirCount>") {
		t.Errorf("genres: %s", body)
	}
	_, _, body = get(t, h, testHost, "/ycast/radiobrowser/language/")
	if !strings.Contains(body, "<Title>Swiss German</Title>") || !strings.Contains(body, "/language/swiss%20german?vtuner=true") {
		t.Errorf("languages: %s", body)
	}
	_, _, body = get(t, h, testHost, "/ycast/radiobrowser/country/")
	if !strings.Contains(body, "<Title>France</Title>") {
		t.Errorf("countries: %s", body)
	}
}

func TestRadioBrowserStationLists(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.ProxyHTTPS = false })
	h := s.Routes()

	_, _, body := get(t, h, testHost, "/ycast/radiobrowser/genre/jazz?vtuner=true&mac=00&startitems=1&enditems=100")
	iA, iB := strings.Index(body, "A Jazz FM"), strings.Index(body, "b jazz radio")
	if iA < 0 || iB < 0 || iA > iB {
		t.Errorf("stations must be sorted by name, case-insensitively:\n%s", body)
	}
	if strings.Contains(body, "Broken Jazz") {
		t.Error("broken stations must be hidden")
	}
	if !strings.Contains(body, "<StationUrl>http://b.example/live</StationUrl>") {
		t.Errorf("https must be downgraded when proxying is off:\n%s", body)
	}
	if !strings.Contains(body, "<ItemCount>2</ItemCount>") {
		t.Errorf("item count:\n%s", body)
	}
	// Paging: second item only.
	_, _, body = get(t, h, testHost, "/ycast/radiobrowser/genre/jazz?startitems=2&enditems=2")
	if strings.Contains(body, "A Jazz FM") || !strings.Contains(body, "b jazz radio") || !strings.Contains(body, "<ItemCount>2</ItemCount>") {
		t.Errorf("paging:\n%s", body)
	}
	// Empty result.
	_, _, body = get(t, h, testHost, "/ycast/radiobrowser/genre/doesnotexist")
	if !strings.Contains(body, "No stations found") {
		t.Errorf("empty list:\n%s", body)
	}
	// Popular keeps API order.
	_, _, body = get(t, h, testHost, "/ycast/radiobrowser/popular/")
	if strings.Index(body, "b jazz radio") > strings.Index(body, "A Jazz FM") {
		t.Errorf("popular order:\n%s", body)
	}
}

func TestStationInfoResolvesAndCountsClicks(t *testing.T) {
	s, f := newTestServer(t, nil)
	h := s.Routes()
	f.resolve["uuid-b"] = "https://b.example/resolved.mp3"

	for i := 1; i <= 2; i++ {
		_, _, body := get(t, h, testHost, "/ycast/station?id=RB_uuid-b")
		want := "<StationUrl>http://denon.vtuner.com/ycast/stream?id=RB_uuid-b</StationUrl>"
		if !strings.Contains(body, want) {
			t.Fatalf("https station must be served through the relay:\n%s", body)
		}
		if got := int(f.urlHits.Load()); got != i {
			t.Errorf("after %d info requests the click endpoint was hit %d times", i, got)
		}
	}
	_, _, body := get(t, h, testHost, "/ycast/station?id=RB_nonexistent")
	if !strings.Contains(body, "Station not found") {
		t.Errorf("unknown station:\n%s", body)
	}
	if code, _, _ := get(t, h, testHost, "/ycast/station"); code != http.StatusBadRequest {
		t.Errorf("missing id: status %d", code)
	}
}

func TestPlayRedirect(t *testing.T) {
	s, _ := newTestServer(t, nil)
	h := s.Routes()
	code, hdr, _ := get(t, h, testHost, "/ycast/play?id=RB_uuid-a")
	if code != http.StatusFound || hdr.Get("Location") != "http://a.example/live" {
		t.Errorf("play http station: %d %q", code, hdr.Get("Location"))
	}
	code, hdr, _ = get(t, h, testHost, "/ycast/play?id=RB_uuid-b")
	if code != http.StatusFound || hdr.Get("Location") != "http://denon.vtuner.com/ycast/stream?id=RB_uuid-b" {
		t.Errorf("play https station: %d %q", code, hdr.Get("Location"))
	}
	if code, _, _ = get(t, h, testHost, "/ycast/play?id=RB_unknown"); code != http.StatusNotFound {
		t.Errorf("play unknown: %d", code)
	}
}

func TestMyStationsPages(t *testing.T) {
	yml := "AC/DC & Co:\n" +
		"  One: http://1.example\n" +
		"  Two: https://2.example\n" +
		"  Three: http://3.example\n" +
		"Plain:\n" +
		"  Jazz Corner: http://jazz.example\n"
	s, _ := newTestServer(t, func(c *Config) { c.StationsFile = writeStationsFile(t, yml); c.ProxyHTTPS = false })
	h := s.Routes()

	_, _, body := get(t, h, testHost, "/ycast/my_stations/")
	if !strings.Contains(body, "<Title>AC/DC &amp; Co</Title>") || !strings.Contains(body, "<DirCount>3</DirCount>") {
		t.Fatalf("categories:\n%s", body)
	}
	// Follow the escaped URL as a receiver would (plus its appended parameters).
	i := strings.Index(body, "<UrlDir>") + len("<UrlDir>")
	dirURL := body[i:strings.Index(body, "</UrlDir>")]
	dirURL = strings.ReplaceAll(dirURL, "&amp;", "&") // what an XML parser hands to the receiver
	dirURL = strings.TrimPrefix(dirURL, "http://"+testHost)
	_, _, body = get(t, h, testHost, dirURL+"&mac=00&startitems=2&enditems=3")
	if strings.Contains(body, "<StationName>One</StationName>") ||
		!strings.Contains(body, "<StationName>Two</StationName>") ||
		!strings.Contains(body, "<StationName>Three</StationName>") ||
		!strings.Contains(body, "<ItemCount>3</ItemCount>") ||
		!strings.Contains(body, "<StationUrl>http://2.example</StationUrl>") ||
		!strings.Contains(body, "<StationDesc>AC/DC &amp; Co</StationDesc>") {
		t.Errorf("category page via escaped url (%s):\n%s", dirURL, body)
	}
	// IDs are the same as the Python version would produce.
	if !strings.Contains(body, "<StationId>MY_"+station.MyChecksum("Two", "https://2.example")+"</StationId>") {
		t.Errorf("station ids:\n%s", body)
	}
}

func TestSearch(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.StationsFile = writeStationsFile(t, "Cat:\n  My Jazz Pick: http://m.example\n") })
	h := s.Routes()
	_, _, body := get(t, h, testHost, "/ycast/search/?search=ja")
	if !strings.Contains(body, "Search query too short") {
		t.Errorf("short query:\n%s", body)
	}
	_, _, body = get(t, h, testHost, "/ycast/search/?search=jazz")
	iMy, iA := strings.Index(body, "My Jazz Pick"), strings.Index(body, "A Jazz FM")
	if iMy < 0 || iA < 0 || iMy > iA || strings.Contains(body, "Broken Jazz") {
		t.Errorf("search results (own stations first, no broken ones):\n%s", body)
	}
	_, _, body = get(t, h, testHost, "/setupapp/denon/asp/BrowseXML/loginXML.asp?search=zzzzzz")
	if !strings.Contains(body, "No stations found") {
		t.Errorf("search without result:\n%s", body)
	}
}

func TestRadioBrowserDown(t *testing.T) {
	s, f := newTestServer(t, nil)
	f.srv.Close()
	_, _, body := get(t, s.Routes(), testHost, "/ycast/radiobrowser/")
	if !strings.Contains(body, "<ItemCount>4</ItemCount>") || !strings.Contains(body, "<DirCount>0</DirCount>") {
		t.Errorf("landing must degrade gracefully when Radio Browser is unreachable:\n%s", body)
	}
	_, _, body = get(t, s.Routes(), testHost, "/ycast/radiobrowser/genre/")
	if !strings.Contains(body, "No entries found") {
		t.Errorf("genres when down:\n%s", body)
	}
}

// ----------------------------------------------------------------- relay

func TestStreamRelay(t *testing.T) {
	payload := make([]byte, 600_000)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	var gotIcyReq atomic.Value
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIcyReq.Store(r.Header.Get("Icy-MetaData"))
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("icy-name", "Test FM")
		w.Header().Set("icy-metaint", "16000")
		for off := 0; off < len(payload); off += 8192 {
			end := min(off+8192, len(payload))
			w.Write(payload[off:end])
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()

	s, f := newTestServer(t, nil)
	s.stream = upstream.Client() // trusts the test certificate
	f.resolve["uuid-s"] = upstream.URL + "/live"
	front := httptest.NewServer(s.Routes())
	defer front.Close()

	req, _ := http.NewRequest("GET", front.URL+"/ycast/stream?id=RB_uuid-s", http.NoBody)
	req.Header.Set("Icy-MetaData", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if sha256.Sum256(body) != sha256.Sum256(payload) {
		t.Errorf("relayed body differs (%d bytes, want %d)", len(body), len(payload))
	}
	if resp.Header.Get("Icy-Name") != "Test FM" || resp.Header.Get("Icy-Metaint") != "16000" || resp.Header.Get("Content-Type") != "audio/mpeg" {
		t.Errorf("headers not forwarded: %v", resp.Header)
	}
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("stream must not use chunked encoding, got %v", resp.TransferEncoding)
	}
	if gotIcyReq.Load() != "1" {
		t.Errorf("Icy-MetaData request header not forwarded: %v", gotIcyReq.Load())
	}

	// Error responses must have their body closed too (bodyclose).
	for _, uri := range []string{"/ycast/stream?id=RB_unknown", "/ycast/stream", "/ycast/stream?id=../../etc/passwd"} {
		r, err := front.Client().Get(front.URL + uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		r.Body.Close()
		switch {
		case uri == "/ycast/stream?id=RB_unknown" && r.StatusCode != http.StatusNotFound:
			t.Errorf("unknown station: %d", r.StatusCode)
		case uri == "/ycast/stream" && r.StatusCode != http.StatusBadRequest:
			t.Errorf("missing id: %d", r.StatusCode)
		case uri == "/ycast/stream?id=../../etc/passwd" && r.StatusCode != http.StatusNotFound:
			t.Errorf("malicious id: %d", r.StatusCode)
		}
	}
}

// ------------------------------------------------------------------ icons

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/2 {
				img.SetNRGBA(x, y, color.NRGBA{200, 30, 30, 255}) // opaque red left half
			} // right half stays fully transparent
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIconEndpointAndCache(t *testing.T) {
	var downloads atomic.Int32
	iconSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write(testPNG(t, 400, 400))
	}))
	defer iconSrv.Close()

	s, f := newTestServer(t, nil)
	f.stations[1]["favicon"] = iconSrv.URL + "/logo.png" // uuid-a
	h := s.Routes()

	for range 2 {
		code, hdr, body := get(t, h, testHost, "/ycast/icon?id=RB_uuid-a")
		if code != 200 || hdr.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("icon: %d %q", code, hdr.Get("Content-Type"))
		}
		cfg, err := jpeg.DecodeConfig(strings.NewReader(body))
		if err != nil || cfg.Width != 290 || cfg.Height != 290 {
			t.Errorf("icon dimensions: %+v %v", cfg, err)
		}
	}
	if downloads.Load() != 1 {
		t.Errorf("icon downloaded %d times, want 1 (second request must come from the cache)", downloads.Load())
	}
	if _, err := os.Stat(filepath.Join(s.cfg.CacheDir, "RB_uuid-a.jpg")); err != nil {
		t.Errorf("cache file missing: %v", err)
	}
	if code, _, _ := get(t, h, testHost, "/ycast/icon?id=RB_uuid-b"); code != http.StatusNotFound {
		t.Errorf("station without icon: %d", code)
	}
	if code, _, _ := get(t, h, testHost, "/ycast/icon?id=..%2F..%2Fetc%2Fpasswd"); code != http.StatusNotFound {
		t.Errorf("path traversal attempt: %d", code)
	}
	if code, _, _ := get(t, h, testHost, "/ycast/icon"); code != http.StatusBadRequest {
		t.Errorf("missing id: %d", code)
	}
}

// ---------------------------------------------------------------- logging

func TestDebugLogsResponses(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := newFakeRB(t)
	cfg := Config{
		ProxyHTTPS:  true,
		MaxStations: 500,
		CacheDir:    filepath.Join(t.TempDir(), "icons"),
		RBAPI:       f.srv.URL,
		Version:     "test",
		Debug:       true,
	}
	s := New(cfg, log)
	srv := httptest.NewServer(s.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ycast/radiobrowser/popular/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, `msg=request`) || !strings.Contains(out, `uri=/ycast/radiobrowser/popular/`) {
		t.Errorf("request not logged in debug mode:\n%s", out)
	}
	if !strings.Contains(out, `msg=response`) || !strings.Contains(out, "ListOfItems") {
		t.Errorf("response body not logged in debug mode:\n%s", out)
	}
	if !strings.Contains(out, "Radio Browser API request") {
		t.Errorf("Radio Browser request should be logged too:\n%s", out)
	}
}

func TestHealthz(t *testing.T) {
	s, _ := newTestServer(t, nil)
	if code, _, body := get(t, s.Routes(), "localhost:8080", "/healthz"); code != 200 || body != "ok\n" {
		t.Errorf("healthz: %d %q", code, body)
	}
}
