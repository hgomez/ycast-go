// Package vtuner generates the XML dialect spoken by vTuner receivers.
//
// The XML is written by hand rather than with encoding/xml on purpose: the
// receivers ship small, naive parsers, so this reproduces exactly what the
// reference implementation (Python ElementTree) emits: no whitespace between
// elements, empty elements as "<X />", and only &, < and > escaped. Do not
// "modernise" this — see docs/DECISIONS.md.
package vtuner

import (
	"strconv"
	"strings"
)

// Exact vTuner XML preamble. Byte-for-byte compatible with the original
// Python output (see DECISIONS.md #2) — do not touch.
const (
	XMLHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>`
	InitToken = `<EncryptedToken>0000000000000000</EncryptedToken>`
)

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func xmlText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1 // not allowed in XML 1.0
		}
		return r
	}, s)
	return xmlEscaper.Replace(s)
}

func writeElem(b *strings.Builder, name, text string) {
	text = xmlText(text)
	if text == "" {
		b.WriteString("<" + name + " />")
		return
	}
	b.WriteString("<" + name + ">" + text + "</" + name + ">")
}

// withBogusParam appends a dummy query parameter. Receivers blindly append
// further parameters with '&' (mac, dlang, startitems...), so the URL needs
// to already contain a '?'.
func withBogusParam(u string) string {
	return u + "?vtuner=true"
}

// Item is an element of a page (a directory, a station or a display line).
type Item interface {
	WriteXML(b *strings.Builder)
}

// Page is a ListOfItems answer: a count and a list of items.
type Page struct {
	Items []Item
	Count int
}

// Add appends an item to the page.
func (p *Page) Add(i Item) { p.Items = append(p.Items, i) }

func (p *Page) String() string {
	var b strings.Builder
	b.WriteString(XMLHeader)
	b.WriteString("<ListOfItems>")
	writeElem(&b, "ItemCount", strconv.Itoa(p.Count))
	for _, i := range p.Items {
		i.WriteXML(&b)
	}
	b.WriteString("</ListOfItems>")
	return b.String()
}

// MessagePage is a page holding a single text line (errors, empty lists).
func MessagePage(text string) *Page {
	p := &Page{Count: 1}
	p.Add(DisplayItem{text})
	return p
}

// DisplayItem is a single text line the receiver shows as-is.
type DisplayItem struct{ Text string }

// WriteXML renders the item into the builder, byte-for-byte compatible with
// the original Python output — do not touch.
func (d DisplayItem) WriteXML(b *strings.Builder) {
	b.WriteString("<Item>")
	writeElem(b, "ItemType", "Display")
	writeElem(b, "Display", d.Text)
	b.WriteString("</Item>")
}

// DirItem is a navigable directory entry.
type DirItem struct {
	Title string
	URL   string
	Count int
}

// WriteXML renders the item into the builder, byte-for-byte compatible with
// the original Python output — do not touch.
func (d DirItem) WriteXML(b *strings.Builder) {
	b.WriteString("<Item>")
	writeElem(b, "ItemType", "Dir")
	writeElem(b, "Title", d.Title)
	writeElem(b, "UrlDir", withBogusParam(d.URL))
	writeElem(b, "UrlDirBackUp", withBogusParam(d.URL))
	writeElem(b, "DirCount", strconv.Itoa(d.Count))
	b.WriteString("</Item>")
}

// StationItem describes a playable station.
type StationItem struct {
	ID, Name, URL, Desc, Logo string
	Genre, Location, Mime     string
	Bitrate                   string
}

// WriteXML renders the item into the builder, byte-for-byte compatible with
// the original Python output — do not touch.
func (s StationItem) WriteXML(b *strings.Builder) {
	b.WriteString("<Item>")
	writeElem(b, "ItemType", "Station")
	writeElem(b, "StationId", s.ID)
	writeElem(b, "StationName", s.Name)
	writeElem(b, "StationUrl", s.URL)
	writeElem(b, "StationDesc", s.Desc)
	writeElem(b, "Logo", s.Logo)
	writeElem(b, "StationFormat", s.Genre)
	writeElem(b, "StationLocation", s.Location)
	writeElem(b, "StationBandWidth", s.Bitrate)
	writeElem(b, "StationMime", s.Mime)
	writeElem(b, "Relia", "3")
	writeElem(b, "Bookmark", "")
	b.WriteString("</Item>")
}
