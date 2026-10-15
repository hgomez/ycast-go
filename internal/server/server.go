// Package server implements the HTTP surface of ycast-go: the vTuner
// navigation endpoints, the search, the station info, the icon endpoint and
// the HTTPS stream relay.
package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hgomez/ycast-go/internal/icons"
	"github.com/hgomez/ycast-go/internal/mystations"
	"github.com/hgomez/ycast-go/internal/radiobrowser"
	"github.com/hgomez/ycast-go/internal/station"
	"github.com/hgomez/ycast-go/internal/vtuner"
)

// Config carries the server options (flags in cmd/ycast-go).
type Config struct {
	StationsFile string
	Listen       string
	ProxyHTTPS   bool
	MaxStations  int
	CacheDir     string
	RBAPI        string
	Version      string
	Debug        bool
}

// Server is the ycast-go HTTP server.
type Server struct {
	cfg    Config
	log    *slog.Logger
	rb     *radiobrowser.Client
	my     *mystations.MyStations
	icons  *icons.Cache
	stream *http.Client

	warnedHosts sync.Map
}

// New wires the server from the configuration.
func New(cfg Config, log *slog.Logger) *Server {
	return &Server{
		cfg:    cfg,
		log:    log,
		rb:     radiobrowser.New(log, cfg.RBAPI, cfg.MaxStations, cfg.Version),
		my:     mystations.New(cfg.StationsFile, log),
		icons:  icons.New(cfg.CacheDir, cfg.Version, log),
		stream: newStreamClient(),
	}
}

// MyCategories exposes the categories of stations.yml (used at startup to
// warn about an unusable configuration file).
func (s *Server) MyCategories() []mystations.Category { return s.my.Categories() }

// Routes returns the HTTP handler with all endpoints wired.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/setupapp/", s.handleUpstream)
	mux.HandleFunc("/{$}", s.handleLanding)
	mux.HandleFunc("/ycast/{$}", s.handleLanding)
	mux.HandleFunc("/ycast/my_stations/{$}", s.handleMyLanding)
	mux.HandleFunc("/ycast/my_stations/{directory}", s.handleMyCategory)
	mux.HandleFunc("/ycast/radiobrowser/{$}", s.handleRBLanding)
	mux.HandleFunc("/ycast/radiobrowser/country/{$}", s.handleRBCountries)
	mux.HandleFunc("/ycast/radiobrowser/country/{directory}", s.handleRBCountry)
	mux.HandleFunc("/ycast/radiobrowser/language/{$}", s.handleRBLanguages)
	mux.HandleFunc("/ycast/radiobrowser/language/{directory}", s.handleRBLanguage)
	mux.HandleFunc("/ycast/radiobrowser/genre/{$}", s.handleRBGenres)
	mux.HandleFunc("/ycast/radiobrowser/genre/{directory}", s.handleRBGenre)
	mux.HandleFunc("/ycast/radiobrowser/popular/{$}", s.handleRBPopular)
	mux.HandleFunc("/ycast/search/{$}", s.handleSearch)
	mux.HandleFunc("/ycast/play", s.handlePlay)
	mux.HandleFunc("/ycast/station", s.handleStationInfo)
	mux.HandleFunc("/ycast/icon", s.handleIcon)
	mux.HandleFunc("/ycast/stream", s.handleStream)
	return s.logRequests(mux)
}

var vtunerHost = regexp.MustCompile(`^[A-Za-z0-9]+\.vtuner\.com$`)

// debugBodyLimit caps how much of a response body is logged in debug mode.
const debugBodyLimit = 8 << 10

// debugWriter tees response bodies into a buffer when debug logging is on.
// The stream relay and icons are excluded (endless or binary), and the buffer
// is capped, so debug mode cannot exhaust memory.
type debugWriter struct {
	http.ResponseWriter
	status int
	buf    *bytes.Buffer // nil: do not capture
}

// WriteHeader records the status for the debug log before delegating.
func (d *debugWriter) WriteHeader(code int) {
	if d.status == 0 {
		d.status = code
	}
	d.ResponseWriter.WriteHeader(code)
}

func (d *debugWriter) Write(p []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	if d.buf != nil && d.buf.Len() < debugBodyLimit {
		n := min(len(p), debugBodyLimit-d.buf.Len())
		d.buf.Write(p[:n])
		if d.buf.Len() >= debugBodyLimit {
			d.buf.WriteString("\n... (truncated)")
			d.buf = nil
		}
	}
	return d.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the Flusher of the real writer
// (needed by the stream relay).
func (d *debugWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.log.Debug("request", "method", r.Method, "host", r.Host, "uri", r.URL.RequestURI(), "remote", r.RemoteAddr)
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !vtunerHost.MatchString(host) {
			if _, seen := s.warnedHosts.LoadOrStore(host, true); !seen {
				s.log.Warn("request with a Host that is not *.vtuner.com; some receivers have problems with this", "host", r.Host)
			}
		}
		if !s.cfg.Debug {
			next.ServeHTTP(w, r)
			return
		}
		dw := &debugWriter{ResponseWriter: w}
		switch r.URL.Path {
		case "/ycast/stream", "/ycast/icon": // endless or binary: log status only
		default:
			dw.buf = bytes.NewBuffer(make([]byte, 0, 2048))
		}
		next.ServeHTTP(dw, r)
		if dw.status == 0 {
			dw.status = http.StatusOK
		}
		if dw.buf != nil && dw.buf.Len() > 0 {
			s.log.Debug("response", "status", dw.status, "uri", r.URL.RequestURI(), "body", dw.buf.String())
		} else {
			s.log.Debug("response", "status", dw.status, "uri", r.URL.RequestURI())
		}
	})
}

// base returns the externally visible root URL, built from the Host header
// the receiver used (so it stays *.vtuner.com when DNS redirection is used).
func (s *Server) base(r *http.Request) string {
	return "http://" + r.Host
}

func writePage(w http.ResponseWriter, p *vtuner.Page) {
	// The reference implementation answers with text/html; receivers are known
	// to work with it, so keep it.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(p.String()))
}

// ------------------------------------------------------------------ paging

func firstQuery(q url.Values, names ...string) (int, bool) {
	for _, n := range names {
		if v := q.Get(n); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return i, true
			}
		}
	}
	return 0, false
}

// paged applies the paging parameters receivers send (startitems/enditems,
// startItems/endItems or start/howmany, all 1-based and inclusive).
func paged[T any](items []T, q url.Values, log *slog.Logger) []T {
	offset := 0
	if v, ok := firstQuery(q, "startitems", "startItems", "start"); ok {
		offset = v - 1
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		log.Warn("paging offset larger than item count")
		return nil
	}
	limit := len(items)
	if v, ok := firstQuery(q, "enditems", "endItems"); ok {
		limit = v
	} else if start, ok := firstQuery(q, "start"); ok {
		if howmany, ok := firstQuery(q, "howmany"); ok {
			limit = start - 1 + howmany
		}
	}
	if limit < offset {
		log.Warn("paging limit smaller than offset")
		return nil
	}
	if limit > len(items) {
		limit = len(items)
	}
	return items[offset:limit]
}

func (s *Server) directoriesPage(r *http.Request, pathPrefix string, dirs []radiobrowser.Directory) *vtuner.Page {
	if len(dirs) == 0 {
		return vtuner.MessagePage("No entries found")
	}
	p := &vtuner.Page{Count: len(dirs)}
	for _, d := range paged(dirs, r.URL.Query(), s.log) {
		p.Add(vtuner.DirItem{Title: d.Display, URL: s.base(r) + pathPrefix + url.PathEscape(d.Name), Count: d.Count})
	}
	return p
}

func (s *Server) stationsPage(r *http.Request, stations []*station.Station) *vtuner.Page {
	if len(stations) == 0 {
		return vtuner.MessagePage("No stations found")
	}
	p := &vtuner.Page{Count: len(stations)}
	for _, st := range paged(stations, r.URL.Query(), s.log) {
		p.Add(s.stationItem(r, st))
	}
	return p
}

// stationURL is the URL the receiver should play. Receivers cannot do TLS, so
// HTTPS streams are either relayed by this server or downgraded to HTTP.
func (s *Server) stationURL(r *http.Request, st *station.Station) string {
	if strings.HasPrefix(strings.ToLower(st.URL), "https://") {
		if s.cfg.ProxyHTTPS {
			return s.base(r) + "/ycast/stream?id=" + url.QueryEscape(st.ID)
		}
		return "http://" + st.URL[len("https://"):]
	}
	return st.URL
}

func (s *Server) stationItem(r *http.Request, st *station.Station) vtuner.StationItem {
	it := vtuner.StationItem{
		ID:       st.ID,
		Name:     st.Name,
		URL:      s.stationURL(r, st),
		Desc:     strings.Join(st.Tags, ", "),
		Logo:     s.base(r) + "/ycast/icon?id=" + url.QueryEscape(st.ID),
		Genre:    st.Genre(),
		Location: st.CountryCode,
		Mime:     st.Codec,
	}
	if st.HasBitrate {
		it.Bitrate = strconv.Itoa(st.Bitrate)
	}
	return it
}

// ---------------------------------------------------------------- stations

// lookupStation finds a station by ID. With resolve set, Radio Browser
// stations get their current playable URL.
func (s *Server) lookupStation(ctx context.Context, id string, resolve bool) *station.Station {
	prefix, rest, ok := station.SplitID(id)
	if !ok {
		return nil
	}
	switch prefix {
	case station.MyPrefix:
		return s.my.ByID(station.MakeID(station.MyPrefix, rest))
	case station.RBPrefix:
		st, err := s.rb.ByUUID(ctx, rest)
		if err != nil {
			s.log.Error("could not get station", "id", id, "err", err)
			return nil
		}
		if st != nil && resolve {
			s.rb.Resolve(ctx, st)
		}
		return st
	}
	return nil
}

// --------------------------------------------------------------- handlers

// handleUpstream serves the legacy /setupapp/... entry points, which are what
// the receivers actually request on denon.vtuner.com & co.
func (s *Server) handleUpstream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := strings.TrimPrefix(r.URL.Path, "/setupapp/")
	switch {
	case q.Get("token") == "0":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(vtuner.InitToken))
	case q.Get("search") != "":
		s.handleSearch(w, r)
	case strings.Contains(path, "statxml.asp") && q.Get("id") != "":
		s.handleStationInfo(w, r)
	case strings.Contains(path, "navXML.asp"):
		s.handleRBLanding(w, r)
	case strings.Contains(path, "FavXML.asp"):
		s.handleMyLanding(w, r)
	case strings.Contains(path, "loginXML.asp"):
		s.handleLanding(w, r)
	default:
		s.log.Error("unhandled upstream query", "path", r.URL.Path)
		http.NotFound(w, r)
	}
}

func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	p := &vtuner.Page{}
	p.Add(vtuner.DirItem{Title: "Radiobrowser", URL: s.base(r) + "/ycast/radiobrowser/", Count: 4})
	if cats := s.my.Categories(); len(cats) > 0 {
		p.Add(vtuner.DirItem{Title: "My Stations", URL: s.base(r) + "/ycast/my_stations/", Count: len(cats)})
	} else {
		p.Add(vtuner.DisplayItem{Text: "'My Stations' feature not configured."})
	}
	p.Count = len(p.Items)
	writePage(w, p)
}

func (s *Server) handleMyLanding(w http.ResponseWriter, r *http.Request) {
	cats := s.my.Categories()
	dirs := make([]radiobrowser.Directory, 0, len(cats))
	for _, c := range cats {
		dirs = append(dirs, radiobrowser.Directory{Name: c.Name, Display: c.Name, Count: len(c.Stations)})
	}
	writePage(w, s.directoriesPage(r, "/ycast/my_stations/", dirs))
}

func (s *Server) handleMyCategory(w http.ResponseWriter, r *http.Request) {
	var stations []*station.Station
	if c := s.my.Category(r.PathValue("directory")); c != nil {
		stations = c.Stations
	}
	writePage(w, s.stationsPage(r, stations))
}

func (s *Server) handleRBLanding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var (
		genres, countries, languages []radiobrowser.Directory
		popular                      []*station.Station
		wg                           sync.WaitGroup
	)
	wg.Add(4)
	// Radio Browser failures degrade to empty directories.
	go func() { defer wg.Done(); var err error; genres, err = s.rb.Genres(ctx); s.warn(err) }()
	go func() { defer wg.Done(); var err error; countries, err = s.rb.Countries(ctx); s.warn(err) }()
	go func() { defer wg.Done(); var err error; languages, err = s.rb.Languages(ctx); s.warn(err) }()
	go func() { defer wg.Done(); var err error; popular, err = s.rb.Popular(ctx); s.warn(err) }()
	wg.Wait()

	b := s.base(r) + "/ycast/radiobrowser/"
	p := &vtuner.Page{Count: 4}
	p.Add(vtuner.DirItem{Title: "Genres", URL: b + "genre/", Count: len(genres)})
	p.Add(vtuner.DirItem{Title: "Countries", URL: b + "country/", Count: len(countries)})
	p.Add(vtuner.DirItem{Title: "Languages", URL: b + "language/", Count: len(languages)})
	p.Add(vtuner.DirItem{Title: "Most Popular", URL: b + "popular/", Count: len(popular)})
	writePage(w, p)
}

func (s *Server) handleRBCountries(w http.ResponseWriter, r *http.Request) {
	dirs, err := s.rb.Countries(r.Context())
	s.warn(err)
	writePage(w, s.directoriesPage(r, "/ycast/radiobrowser/country/", dirs))
}

func (s *Server) handleRBLanguages(w http.ResponseWriter, r *http.Request) {
	dirs, err := s.rb.Languages(r.Context())
	s.warn(err)
	writePage(w, s.directoriesPage(r, "/ycast/radiobrowser/language/", dirs))
}

func (s *Server) handleRBGenres(w http.ResponseWriter, r *http.Request) {
	dirs, err := s.rb.Genres(r.Context())
	s.warn(err)
	writePage(w, s.directoriesPage(r, "/ycast/radiobrowser/genre/", dirs))
}

func (s *Server) handleRBCountry(w http.ResponseWriter, r *http.Request) {
	st, err := s.rb.ByCountry(r.Context(), r.PathValue("directory"))
	s.warn(err)
	writePage(w, s.stationsPage(r, st))
}

func (s *Server) handleRBLanguage(w http.ResponseWriter, r *http.Request) {
	st, err := s.rb.ByLanguage(r.Context(), r.PathValue("directory"))
	s.warn(err)
	writePage(w, s.stationsPage(r, st))
}

func (s *Server) handleRBGenre(w http.ResponseWriter, r *http.Request) {
	st, err := s.rb.ByGenre(r.Context(), r.PathValue("directory"))
	s.warn(err)
	writePage(w, s.stationsPage(r, st))
}

func (s *Server) handleRBPopular(w http.ResponseWriter, r *http.Request) {
	st, err := s.rb.Popular(r.Context())
	s.warn(err)
	writePage(w, s.stationsPage(r, st))
}

func (s *Server) warn(err error) {
	if err != nil {
		s.log.Error("Radio Browser request failed", "err", err)
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("search")
	if utf8.RuneCountInString(query) < 3 {
		writePage(w, vtuner.MessagePage("Search query too short"))
		return
	}
	stations := s.my.Search(query) // own stations first
	rb, err := s.rb.Search(r.Context(), query)
	s.warn(err)
	stations = append(stations, rb...)
	writePage(w, s.stationsPage(r, stations))
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing station id", http.StatusBadRequest)
		return
	}
	st := s.lookupStation(r.Context(), id, true)
	if st == nil {
		s.log.Error("could not get station", "id", id)
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, s.stationURL(r, st), http.StatusFound)
}

func (s *Server) handleStationInfo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing station id", http.StatusBadRequest)
		return
	}
	st := s.lookupStation(r.Context(), id, true)
	if st == nil {
		s.log.Error("could not get station", "id", id)
		writePage(w, vtuner.MessagePage("Station not found"))
		return
	}
	p := &vtuner.Page{Count: 1}
	p.Add(s.stationItem(r, st))
	writePage(w, p)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing station id", http.StatusBadRequest)
		return
	}
	st := s.lookupStation(r.Context(), id, false)
	if st == nil || st.Icon == "" {
		http.NotFound(w, r)
		return
	}
	data, err := s.icons.Get(r.Context(), st)
	if err != nil {
		s.log.Warn("could not get station icon", "id", id, "err", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(data)
}
