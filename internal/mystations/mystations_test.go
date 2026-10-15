package mystations

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hgomez/ycast-go/internal/station"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func writeStationsFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stations.yml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseStationsYAML(t *testing.T) {
	src := "\xef\xbb\xbf# comment\r\n" +
		"Electronic:   # trailing comment\r\n" +
		"  Deep House: http://a.example:8006\r\n" +
		"  \"Quoted: name\": 'http://b.example/it''s'  # c\r\n" +
		"  Plain: \"http://c.example/\\\"x\\\"\"\r\n" +
		"\r\n" +
		"Chillout:\n" +
		"  SomaFM: https://ice1.somafm.com/x\n" +
		"  Deep House: http://replaced.example\n"
	cats, err := ParseYAML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 || cats[0].Name != "Electronic" || cats[1].Name != "Chillout" {
		t.Fatalf("categories: %+v", cats)
	}
	got := make([][2]string, 0, len(cats[0].Stations))
	for _, s := range cats[0].Stations {
		got = append(got, [2]string{s.Name, s.URL})
	}
	want := [][2]string{
		{"Deep House", "http://a.example:8006"},
		{"Quoted: name", "http://b.example/it's"},
		{"Plain", `http://c.example/"x"`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stations = %v, want %v", got, want)
	}
	if cats[1].Stations[1].URL != "http://replaced.example" {
		t.Errorf("station in another category must be independent: %+v", cats[1].Stations)
	}
	if id := cats[0].Stations[0].ID; id != "MY_"+station.MyChecksum("Deep House", "http://a.example:8006") {
		t.Errorf("unexpected id %s", id)
	}
}

func TestParseStationsYAMLDuplicateKeepsPosition(t *testing.T) {
	cats, err := ParseYAML([]byte("C:\n  A: http://1\n  B: http://2\n  A: http://3\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := cats[0].Stations
	if len(s) != 2 || s[0].Name != "A" || s[0].URL != "http://3" || s[1].Name != "B" {
		t.Errorf("got %+v", s)
	}
}

func TestParseStationsYAMLErrors(t *testing.T) {
	bad := map[string]string{
		"tab indent":         "Cat:\n\tA: http://x\n",
		"station no cat":     "  A: http://x\n",
		"category has value": "Cat: http://x\n",
		"no colon":           "Cat:\n  just text\n",
		"nested":             "Cat:\n  Sub:\n",
		"unterminated quote": "Cat:\n  \"A: http://x\n",
		"text after quote":   "Cat:\n  A: \"http://x\" junk\n",
	}
	for name, src := range bad {
		if _, err := ParseYAML([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMyStationsHotReload(t *testing.T) {
	path := writeStationsFile(t, "One:\n  A: http://a.example\n")
	m := New(path, discardLogger())
	if c := m.Categories(); len(c) != 1 || c[0].Name != "One" {
		t.Fatalf("initial: %+v", c)
	}
	// Valid change is picked up.
	os.WriteFile(path, []byte("One:\n  A: http://a.example\nTwo:\n  B: http://b.example\n"), 0o644)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(path, future, future)
	if c := m.Categories(); len(c) != 2 {
		t.Fatalf("after edit: %+v", c)
	}
	// A broken edit keeps the last good configuration.
	os.WriteFile(path, []byte("Broken:\n\tx\n"), 0o644)
	future = future.Add(2 * time.Second)
	os.Chtimes(path, future, future)
	if c := m.Categories(); len(c) != 2 {
		t.Fatalf("after broken edit the last good config should stay: %+v", c)
	}
	// A removed file disables the feature.
	os.Remove(path)
	if c := m.Categories(); len(c) != 0 {
		t.Fatalf("after removal: %+v", c)
	}
}
