package icons

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

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

func TestConvertIcon(t *testing.T) {
	out, err := convert(testPNG(t, 600, 300))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 290 || b.Dy() != 145 {
		t.Errorf("large icon: size %dx%d, want 290x145", b.Dx(), b.Dy())
	}
	// Transparent area must end up white, opaque area red.
	r, g, bl, _ := img.At(280, 70).RGBA()
	if r>>8 < 240 || g>>8 < 240 || bl>>8 < 240 {
		t.Errorf("transparent area should be white, got %d,%d,%d", r>>8, g>>8, bl>>8)
	}
	r, g, _, _ = img.At(10, 70).RGBA()
	if r>>8 < 150 || g>>8 > 90 {
		t.Errorf("opaque area should stay red, got %d,%d", r>>8, g>>8)
	}

	out, err = convert(testPNG(t, 100, 50))
	if err != nil {
		t.Fatal(err)
	}
	img, _ = jpeg.Decode(bytes.NewReader(out))
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Errorf("small icon must keep its size, got %dx%d", b.Dx(), b.Dy())
	}
	if _, err := convert([]byte("definitely not an image")); err == nil {
		t.Error("garbage must be rejected")
	}
}
