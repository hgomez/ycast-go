// Package radiobrowser is a client for the Community Radio Browser API
// (radio-browser.info): server discovery through DNS, response caching and
// graceful degradation.
package radiobrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hgomez/ycast-go/internal/station"
)

const (
	discoveryHost    = "all.api.radio-browser.info"
	minimumCount     = 5   // directories with fewer stations are hidden
	defaultLimit     = 200 // search and "most popular" result size
	cacheTTL         = 5 * time.Minute
	serverListTTL    = time.Hour
	maxCacheEntries  = 64
	maxResponseBytes = 32 << 20
)

// Directory is a navigable group of stations (a country, a language, a tag).
type Directory struct {
	Name    string // value used in URLs and API queries
	Display string
	Count   int
}

// Client talks to the Community Radio Browser API.
type Client struct {
	log         *slog.Logger
	http        *http.Client
	fixedBase   string // -rb-api override (also used by the tests)
	maxStations int    // cap for country/language/genre listings
	showBroken  bool
	userAgent   string

	mu          sync.Mutex
	servers     []string
	serversAt   time.Time
	cache       map[string]cacheEntry
	lookupHosts func(ctx context.Context) []string // overridable in tests
}

type cacheEntry struct {
	data    []byte
	expires time.Time
}

// New returns a client. An empty fixedBase enables DNS-based discovery.
func New(log *slog.Logger, fixedBase string, maxStations int, version string) *Client {
	return &Client{
		log:         log,
		http:        &http.Client{Timeout: 20 * time.Second},
		fixedBase:   strings.TrimRight(fixedBase, "/"),
		maxStations: maxStations,
		userAgent:   "YCast-go/" + version,
		cache:       map[string]cacheEntry{},
	}
}

// serverList returns the base URLs of the available API servers. As the
// Radio Browser project recommends, the servers are discovered through DNS
// (all.api.radio-browser.info) and contacted in random order.
func (c *Client) serverList(ctx context.Context) []string {
	if c.fixedBase != "" {
		return []string{c.fixedBase}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.servers) > 0 && time.Since(c.serversAt) < serverListTTL {
		return append([]string(nil), c.servers...)
	}
	var names []string
	if c.lookupHosts != nil {
		names = c.lookupHosts(ctx)
	} else {
		names = discoverHosts(ctx)
	}
	servers := make([]string, 0, len(names)+1)
	for _, n := range names {
		servers = append(servers, "https://"+n)
	}
	servers = append(servers, "https://"+discoveryHost)
	c.servers, c.serversAt = servers, time.Now()
	return append([]string(nil), servers...)
}

func discoverHosts(ctx context.Context) []string {
	ips, err := net.DefaultResolver.LookupHost(ctx, discoveryHost)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, ip := range ips {
		addrs, err := net.DefaultResolver.LookupAddr(ctx, ip)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			a = strings.TrimSuffix(a, ".")
			if a != "" && !seen[a] {
				seen[a] = true
				names = append(names, a)
			}
		}
	}
	return names
}

// getJSON fetches /json/<path>?<query> and decodes it into out. Responses are
// cached for a few minutes unless ttl is zero.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, ttl time.Duration, out any) error {
	key := path + "?" + q.Encode()
	if ttl > 0 {
		c.mu.Lock()
		e, ok := c.cache[key]
		c.mu.Unlock()
		if ok && time.Now().Before(e.expires) {
			return json.Unmarshal(e.data, out)
		}
	}
	servers := c.serverList(ctx)
	if c.fixedBase == "" {
		rand.Shuffle(len(servers)-1, func(i, j int) { servers[i], servers[j] = servers[j], servers[i] })
	}
	var lastErr error
	for i, base := range servers {
		if i >= 3 {
			break
		}
		data, err := c.fetch(ctx, base+"/json/"+path+"?"+q.Encode())
		if err != nil {
			lastErr = err
			c.log.Warn("Radio Browser request failed", "server", base, "err", err)
			continue
		}
		if ttl > 0 {
			c.storeCache(key, data, ttl)
		}
		return json.Unmarshal(data, out)
	}
	if lastErr == nil {
		lastErr = errors.New("no Radio Browser server available")
	}
	return lastErr
}

func (c *Client) fetch(ctx context.Context, u string) ([]byte, error) {
	c.log.Debug("Radio Browser API request", "url", u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

func (c *Client) storeCache(key string, data []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.cache) >= maxCacheEntries {
		for k, e := range c.cache { // drop expired entries first
			if now.After(e.expires) {
				delete(c.cache, k)
			}
		}
		for k := range c.cache { // still full: drop an arbitrary one
			if len(c.cache) < maxCacheEntries {
				break
			}
			delete(c.cache, k)
		}
	}
	c.cache[key] = cacheEntry{data: data, expires: now.Add(ttl)}
}

// ---------------------------------------------------------------- stations

type rbStationJSON struct {
	UUID        string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLResolved string `json:"url_resolved"`
	Favicon     string `json:"favicon"`
	Tags        string `json:"tags"`
	CountryCode string `json:"countrycode"`
	Language    string `json:"language"`
	Votes       int    `json:"votes"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	LastCheckOK int    `json:"lastcheckok"`
}

func (j rbStationJSON) toStation() *station.Station {
	var tags []string
	for _, t := range strings.Split(j.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	u := j.URLResolved
	if u == "" {
		u = j.URL
	}
	return &station.Station{
		ID:          station.MakeID(station.RBPrefix, j.UUID),
		Name:        strings.TrimSpace(j.Name),
		URL:         u,
		Icon:        j.Favicon,
		Tags:        tags,
		CountryCode: j.CountryCode,
		Language:    j.Language,
		Codec:       j.Codec,
		Votes:       j.Votes,
		Bitrate:     j.Bitrate,
		HasBitrate:  true,
	}
}

func (c *Client) stationsQuery(ctx context.Context, path string, q url.Values, byName bool) ([]*station.Station, error) {
	if !c.showBroken {
		q.Set("hidebroken", "true")
	}
	var raw []rbStationJSON
	if err := c.getJSON(ctx, path, q, cacheTTL, &raw); err != nil {
		return nil, err
	}
	out := make([]*station.Station, 0, len(raw))
	for i := range raw {
		j := &raw[i] // index the slice: each entry is ~168 bytes, copying it per iteration is wasteful
		if j.UUID == "" || (!c.showBroken && j.LastCheckOK != 1) {
			continue
		}
		out = append(out, j.toStation())
	}
	if byName {
		sort.SliceStable(out, func(a, b int) bool {
			return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
		})
	}
	return out, nil
}

// ByUUID looks a station up by its Radio Browser UUID. A station that does
// not exist (or is unusable) is reported as (nil, nil), not as an error:
// callers treat it as an empty result, not a failure.
func (c *Client) ByUUID(ctx context.Context, uuid string) (*station.Station, error) {
	var raw []rbStationJSON
	if err := c.getJSON(ctx, "stations/byuuid/"+url.PathEscape(uuid), url.Values{}, cacheTTL, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[0].UUID == "" {
		return nil, nil //nolint:nilnil // (nil, nil) deliberately means "station not found", not an error
	}
	return raw[0].toStation(), nil
}

// Resolve replaces the station URL with the current playable one. This call
// also registers a "click" for the station, as the API asks clients to do,
// so it is never cached.
func (c *Client) Resolve(ctx context.Context, s *station.Station) {
	_, uuid, ok := station.SplitID(s.ID)
	if !ok {
		return
	}
	var data json.RawMessage
	if err := c.getJSON(ctx, "url/"+url.PathEscape(uuid), url.Values{}, 0, &data); err != nil {
		c.log.Error("could not retrieve playable URL", "station", s.ID, "err", err)
		return
	}
	// The API returns an object; older versions returned a list of objects.
	var one struct {
		URL string `json:"url"`
	}
	var list []struct {
		URL string `json:"url"`
	}
	switch {
	case json.Unmarshal(data, &one) == nil && one.URL != "":
		s.URL = one.URL
	case json.Unmarshal(data, &list) == nil && len(list) > 0 && list[0].URL != "":
		s.URL = list[0].URL
	default:
		c.log.Error("could not retrieve first playlist item", "station", s.ID)
	}
}

// Search looks stations up by (partial) name, most voted first.
func (c *Client) Search(ctx context.Context, name string) ([]*station.Station, error) {
	q := url.Values{}
	q.Set("order", "votes")
	q.Set("reverse", "true")
	q.Set("limit", strconv.Itoa(defaultLimit))
	q.Set("name", name)
	return c.stationsQuery(ctx, "stations/search", q, true)
}

// Popular returns the most voted stations.
func (c *Client) Popular(ctx context.Context) ([]*station.Station, error) {
	q := url.Values{}
	q.Set("order", "votes")
	q.Set("reverse", "true")
	q.Set("limit", strconv.Itoa(defaultLimit))
	return c.stationsQuery(ctx, "stations", q, false)
}

// exactQuery lists stations for a country/language/genre. To keep responses
// small enough for home routers, only the c.maxStations most voted stations
// are fetched; they are then sorted by name.
func (c *Client) exactQuery(ctx context.Context, field, value string) ([]*station.Station, error) {
	q := url.Values{}
	q.Set("order", "votes")
	q.Set("reverse", "true")
	q.Set("limit", strconv.Itoa(c.maxStations))
	q.Set(field+"Exact", "true")
	q.Set(field, value)
	return c.stationsQuery(ctx, "stations/search", q, true)
}

// ByCountry lists the stations of one country (name-sorted, capped to
// maxStations).
func (c *Client) ByCountry(ctx context.Context, v string) ([]*station.Station, error) {
	return c.exactQuery(ctx, "country", v)
}

// ByLanguage lists the stations of one language (name-sorted, capped to
// maxStations).
func (c *Client) ByLanguage(ctx context.Context, v string) ([]*station.Station, error) {
	return c.exactQuery(ctx, "language", v)
}

// ByGenre lists the stations of one tag (name-sorted, capped to
// maxStations).
func (c *Client) ByGenre(ctx context.Context, v string) ([]*station.Station, error) {
	return c.exactQuery(ctx, "tag", v)
}

// -------------------------------------------------------------- directories

func (c *Client) directories(ctx context.Context, path string, display func(string) string) ([]Directory, error) {
	q := url.Values{}
	if !c.showBroken {
		q.Set("hidebroken", "true")
	}
	var raw []rbDirJSON
	if err := c.getJSON(ctx, path, q, cacheTTL, &raw); err != nil {
		return nil, err
	}
	var out []Directory
	for _, d := range raw {
		if d.Name == "" || d.StationCount <= minimumCount {
			continue
		}
		n := d.StationCount
		if n > c.maxStations {
			n = c.maxStations
		}
		out = append(out, Directory{Name: d.Name, Display: display(d.Name), Count: n})
	}
	return out, nil
}

type rbDirJSON struct {
	Name         string `json:"name"`
	StationCount int    `json:"stationcount"`
}

func identity(s string) string { return s }

// Countries lists the country directory (hidden below minimumCount stations,
// counts capped to maxStations).
func (c *Client) Countries(ctx context.Context) ([]Directory, error) {
	return c.directories(ctx, "countries", identity)
}

// Languages lists the language directory (display names title-cased).
func (c *Client) Languages(ctx context.Context) ([]Directory, error) {
	return c.directories(ctx, "languages", pyTitle)
}

// Genres lists the tag directory (display names capitalized).
func (c *Client) Genres(ctx context.Context) ([]Directory, error) {
	return c.directories(ctx, "tags", pyCapitalize)
}

// pyTitle mimics Python's str.title(): every run of letters starts with an
// upper case letter, the rest is lower case.
func pyTitle(s string) string {
	var b strings.Builder
	prevLetter := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if prevLetter {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToUpper(r))
			}
			prevLetter = true
		} else {
			b.WriteRune(r)
			prevLetter = false
		}
	}
	return b.String()
}

// pyCapitalize mimics Python's str.capitalize().
func pyCapitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + strings.ToLower(s[size:])
}
