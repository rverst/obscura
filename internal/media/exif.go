package media

import (
	"errors"
	"io"
	"time"

	"github.com/evanoberholster/imagemeta"
	"github.com/evanoberholster/imagemeta/meta"
)

// Metadata is what obscura keeps about a file. It all lands in the encrypted
// database, never in the derivatives served to a browser.
type Metadata struct {
	Width       int
	Height      int
	Orientation int // EXIF orientation, 1..8

	TakenAt *time.Time
	// CameraMake is canonicalised by the parser: bodies that write "SONY",
	// "Sony" or "SONY CORPORATION" all come back as "Sony", so search and
	// filtering see one brand rather than three.
	CameraMake  string
	CameraModel string
	Lens        string
	ISO         int
	FNumber     float64
	Exposure    string
	FocalLen    float64

	// Pointers rather than zero values: 0,0 is a real place in the Gulf of
	// Guinea, so "no coordinates" has to be distinguishable from the equator.
	GPSLat *float64
	GPSLon *float64
}

// ExtractMetadata reads EXIF from an image.
//
// A file without EXIF is normal, not an error -- screenshots, exports and
// anything that has been through a stripping tool have none. In that case the
// zero Metadata comes back and the caller falls back to the upload time.
//
// Parsing is pure Go and reads only the header, so this does not decode the
// image and costs almost nothing even for a 24 MP frame.
func ExtractMetadata(r io.ReadSeeker) (Metadata, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, err
	}

	ex, err := imagemeta.Decode(r)
	if err != nil {
		if errors.Is(err, imagemeta.ErrNoExif) {
			return Metadata{}, nil
		}
		// A file obscura cannot parse EXIF from may still be a perfectly good
		// image, so this is not fatal for the upload.
		return Metadata{}, nil
	}

	m := Metadata{
		Width:       int(ex.ExifIFD.PixelXDimension),
		Height:      int(ex.ExifIFD.PixelYDimension),
		Orientation: orientationValue(ex.IFD0.Orientation),
		CameraMake:  ex.IFD0.Make,
		CameraModel: ex.IFD0.Model,
		Lens:        ex.ExifIFD.LensModel,
		ISO:         int(ex.ExifIFD.ISOSpeedRatings),
		FNumber:     float64(ex.ExifIFD.FNumber),
		FocalLen:    float64(ex.ExifIFD.FocalLength),
	}
	if m.Width == 0 {
		m.Width = int(ex.IFD0.ImageWidth)
	}
	if m.Height == 0 {
		m.Height = int(ex.IFD0.ImageHeight)
	}
	if s := ex.ExifIFD.ExposureTime.String(); s != "" && s != "0" {
		m.Exposure = s
	}

	// DateTimeOriginal is when the shutter fired; CreateDate can be later, for
	// instance when a raw file was converted. Prefer the former.
	if t := ex.ExifIFD.DateTimeOriginal; !t.IsZero() {
		m.TakenAt = &t
	} else if t := ex.ExifIFD.CreateDate; !t.IsZero() {
		m.TakenAt = &t
	} else if t := ex.IFD0.ModifyDate; !t.IsZero() {
		m.TakenAt = &t
	}

	if lat, lon := ex.GPS.Latitude(), ex.GPS.Longitude(); lat != 0 || lon != 0 {
		m.GPSLat, m.GPSLon = &lat, &lon
	}
	return m, nil
}

// SwapsDimensions reports whether an EXIF orientation rotates the image by a
// quarter turn, meaning stored width and height are the wrong way round for
// display.
//
// vipsthumbnail applies the rotation when generating derivatives, so the
// database has to record the displayed dimensions -- otherwise the grid would
// lay out portrait photos in landscape boxes.
func SwapsDimensions(orientation int) bool {
	switch orientation {
	case 5, 6, 7, 8:
		return true
	default:
		return false
	}
}

func orientationValue(o meta.Orientation) int {
	v := int(o)
	if v < 1 || v > 8 {
		return 1 // unset or nonsense: treat as upright
	}
	return v
}
