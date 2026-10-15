// Package station defines the Station type shared by every source
// (stations.yml, Radio Browser) and the station ID scheme. The IDs are
// computed exactly like in the original YCast Python so presets stored on
// receivers stay valid.
package station

import (
	"crypto/md5"
	"encoding/hex"
	"regexp"
	"strings"
)

// Station is a radio station from any source.
type Station struct {
	ID, Name, URL, Icon string
	Tags                []string
	CountryCode         string
	Language            string
	Codec               string
	Votes               int
	Bitrate             int
	HasBitrate          bool
}

// Genre returns the first tag, or "" when the station has none.
func (s *Station) Genre() string {
	if len(s.Tags) == 0 {
		return ""
	}
	return s.Tags[0]
}

// Station ID prefixes: MyPrefix marks stations from stations.yml, RBPrefix
// stations from Radio Browser.
const (
	MyPrefix = "MY" // stations from stations.yml
	RBPrefix = "RB" // stations from Radio Browser
)

// validID restricts station IDs to a safe alphabet. IDs end up in file names
// (icon cache) and in upstream API paths, so anything else is refused.
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{4,128}$`)

// MakeID combines a source prefix and a source-specific identifier.
func MakeID(prefix, uid string) string {
	return prefix + "_" + uid
}

// SplitID splits "XX_<id>" into its prefix and the rest.
func SplitID(id string) (prefix, rest string, ok bool) {
	if !validID.MatchString(id) || id[2] != '_' {
		return "", "", false
	}
	return id[:2], id[3:], true
}

// MyChecksum derives the ID of a station from stations.yml. The algorithm is
// the same as in the original YCast (MD5 of name+URL, second half of the
// digest XOR-folded onto the first half, first 12 hex chars, upper case), so
// IDs - and therefore presets stored on the receiver - stay valid when
// switching from the Python version.
func MyChecksum(name, url string) string {
	d := md5.Sum([]byte(name + url))
	var folded [8]byte
	copy(folded[:], d[:8])
	for i, b := range d[8:] {
		folded[i] ^= b
	}
	return strings.ToUpper(hex.EncodeToString(folded[:]))[:12]
}
