// Package icons fetches station logos, converts them to small JPEGs (the
// format vTuner receivers expect) and keeps the result on disk.
package icons

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hgomez/ycast-go/internal/station"
	xdraw "golang.org/x/image/draw"
)

const (
	maxSide      = 290      // longest side of the JPEG sent to the receiver
	maxDownload  = 5 << 20  // refuse icons larger than this
	maxPixels    = 16 << 20 // refuse images with more pixels than this
	jpegQuality  = 85
	fetchTimeout = 10 * time.Second
)

// Cache is an on-disk icon cache.
type Cache struct {
	dir       string // empty: no caching
	log       *slog.Logger
	http      *http.Client
	userAgent string
}

// New returns the cache. An empty dir disables caching.
func New(dir, version string, log *slog.Logger) *Cache {
	return &Cache{
		dir:       dir,
		log:       log,
		http:      &http.Client{Timeout: fetchTimeout},
		userAgent: "YCast-go/" + version,
	}
}

// Get returns the icon of the station as a small JPEG, downloading and
// converting it on the first call.
func (c *Cache) Get(ctx context.Context, s *station.Station) ([]byte, error) {
	if s.Icon == "" {
		return nil, errors.New("station has no icon")
	}
	var file string
	if c.dir != "" {
		file = filepath.Join(c.dir, s.ID+".jpg")
		// Safe: s.ID is validated by validStationID (restricted alphabet,
		// see internal/station) — IDs never contain path separators.
		if data, err := os.ReadFile(file); err == nil { //nolint:gosec // G304: validated station ID, see comment above
			return data, nil
		}
	}
	c.log.Debug("icon cache miss", "station", s.ID, "icon", s.Icon)
	raw, err := c.download(ctx, s.Icon)
	if err != nil {
		return nil, err
	}
	data, err := convert(raw)
	if err != nil {
		return nil, err
	}
	if file != "" {
		if err := writeFileAtomic(file, data); err != nil {
			c.log.Warn("could not write icon cache", "file", file, "err", err)
		}
	}
	return data, nil
}

func (c *Cache) download(ctx context.Context, u string) ([]byte, error) {
	if !strings.HasPrefix(strings.ToLower(u), "http://") && !strings.HasPrefix(strings.ToLower(u), "https://") {
		return nil, fmt.Errorf("unsupported icon URL scheme: %s", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icon download from %s: HTTP status %d", u, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, errors.New("icon too large")
	}
	return data, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".icon-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name()) // best-effort cleanup, the write already failed
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name()) // best-effort cleanup, the rename already failed
		return err
	}
	return nil
}

// convert decodes a PNG/JPEG/GIF image, flattens transparency onto white,
// shrinks it so that its longest side is at most maxSide (smaller images keep
// their size) and re-encodes it as JPEG.
func convert(raw []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("unsupported icon format: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, errors.New("icon dimensions out of range")
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("icon decoding failed: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	// Flatten onto white.
	flat := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(flat, flat.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), src, b.Min, draw.Over)

	nw, nh := w, h
	if w > maxSide || h > maxSide {
		if w >= h {
			nw, nh = maxSide, max(1, h*maxSide/w)
		} else {
			nw, nh = max(1, w*maxSide/h), maxSide
		}
	}
	out := flat
	if nw != w || nh != h {
		out = image.NewRGBA(image.Rect(0, 0, nw, nh))
		// CatmullRom gives the best downscaling quality of the scalers in
		// x/image; icons are tiny, the extra cost is irrelevant.
		xdraw.CatmullRom.Scale(out, out.Bounds(), flat, flat.Bounds(), draw.Src, nil)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
