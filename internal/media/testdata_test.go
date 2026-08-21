package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// jpegNoExif renders a plain JPEG with no metadata attached.
func jpegNoExif(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// A gradient rather than flat colour, so the encoder produces
			// something with realistic entropy instead of a degenerate file.
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// exifFixture describes the metadata to bake into a test JPEG.
type exifFixture struct {
	make_       string
	model       string
	orientation uint16
	iso         uint16
	dateTime    string // "2006:01:02 15:04:05"
	pixelX      uint32
	pixelY      uint32
}

// jpegWithExif renders a JPEG carrying a hand-built EXIF APP1 segment.
//
// The block is assembled by hand rather than copied from a binary fixture so
// the test states exactly which tags it expects the parser to read. Anything
// this asserts is therefore traceable to a concrete tag id.
func jpegWithExif(t *testing.T, w, h int, f exifFixture) []byte {
	t.Helper()

	const (
		tagMake        = 0x010F
		tagModel       = 0x0110
		tagOrientation = 0x0112
		tagExifIFD     = 0x8769
		tagISO         = 0x8827
		tagDateTimeOrg = 0x9003
		tagPixelX      = 0xA002
		tagPixelY      = 0xA003

		typeASCII = 2
		typeShort = 3
		typeLong  = 4
	)

	makeB := append([]byte(f.make_), 0)
	modelB := append([]byte(f.model), 0)
	dateB := append([]byte(f.dateTime), 0)

	const (
		ifd0Entries = 4
		exifEntries = 4
		headerLen   = 8
	)
	ifd0Len := 2 + ifd0Entries*12 + 4
	exifLen := 2 + exifEntries*12 + 4

	ifd0Off := uint32(headerLen)
	exifOff := ifd0Off + uint32(ifd0Len)
	dataOff := exifOff + uint32(exifLen)

	makeOff := dataOff
	modelOff := makeOff + uint32(len(makeB))
	dateOff := modelOff + uint32(len(modelB))

	le := binary.LittleEndian
	entry := func(b *bytes.Buffer, tag, typ uint16, count, value uint32) {
		binary.Write(b, le, tag)
		binary.Write(b, le, typ)
		binary.Write(b, le, count)
		binary.Write(b, le, value)
	}
	// Values of four bytes or fewer live inline, left-aligned in the field.
	inlineShort := func(v uint16) uint32 { return uint32(v) }

	var tiff bytes.Buffer
	tiff.WriteString("II")              // little endian
	binary.Write(&tiff, le, uint16(42)) // TIFF magic
	binary.Write(&tiff, le, ifd0Off)

	// --- IFD0 ---
	binary.Write(&tiff, le, uint16(ifd0Entries))
	entry(&tiff, tagMake, typeASCII, uint32(len(makeB)), makeOff)
	entry(&tiff, tagModel, typeASCII, uint32(len(modelB)), modelOff)
	entry(&tiff, tagOrientation, typeShort, 1, inlineShort(f.orientation))
	entry(&tiff, tagExifIFD, typeLong, 1, exifOff)
	binary.Write(&tiff, le, uint32(0)) // no IFD1

	// --- ExifIFD ---
	binary.Write(&tiff, le, uint16(exifEntries))
	entry(&tiff, tagISO, typeShort, 1, inlineShort(f.iso))
	entry(&tiff, tagDateTimeOrg, typeASCII, uint32(len(dateB)), dateOff)
	entry(&tiff, tagPixelX, typeLong, 1, f.pixelX)
	entry(&tiff, tagPixelY, typeLong, 1, f.pixelY)
	binary.Write(&tiff, le, uint32(0))

	// --- data area ---
	tiff.Write(makeB)
	tiff.Write(modelB)
	tiff.Write(dateB)

	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	if len(app1)+2 > 0xFFFF {
		t.Fatalf("EXIF block too large: %d bytes", len(app1))
	}

	base := jpegNoExif(t, w, h)
	if !bytes.HasPrefix(base, []byte{0xFF, 0xD8}) {
		t.Fatal("encoder did not produce an SOI marker")
	}

	var out bytes.Buffer
	out.Write(base[:2]) // SOI
	out.Write([]byte{0xFF, 0xE1})
	binary.Write(&out, binary.BigEndian, uint16(len(app1)+2)) // JPEG lengths are big endian
	out.Write(app1)
	out.Write(base[2:])
	return out.Bytes()
}
