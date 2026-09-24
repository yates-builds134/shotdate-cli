package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// TIFF/EXIF tag IDs we care about. See the EXIF 2.32 spec, section 4.6.4.
const (
	tagDateTime          = 0x0132 // IFD0: file change date, weakest signal
	tagExifIFDPointer    = 0x8769 // IFD0: offset to the Exif sub-IFD
	tagDateTimeOriginal  = 0x9003 // Exif sub-IFD: when the shutter fired
	tagDateTimeDigitized = 0x9004 // Exif sub-IFD: when the file was written
)

const (
	tiffTypeASCII = 2
	tiffTypeShort = 3
	tiffTypeLong  = 4
)

// CaptureDate is a timestamp pulled from an image, tagged with where it came
// from so a caller can judge how much to trust it.
type CaptureDate struct {
	Value  string // EXIF format: "YYYY:MM:DD HH:MM:SS"
	Source string
}

// ReadCaptureDate returns the best capture timestamp it can find for the
// image at path.
//
// In strict mode (lenient == false) it only ever returns a value backed by
// the DateTimeOriginal tag, and only if no other timestamp tag in the file
// contradicts it. Anything else - a missing tag, a corrupt segment, a file
// with no EXIF data at all, disagreeing tags - is an error.
//
// In lenient mode it degrades gracefully: DateTimeDigitized, then the plain
// DateTime tag, then finally the filesystem mtime.
func ReadCaptureDate(path string, lenient bool) (*CaptureDate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	block, err := extractEXIFBlock(f)
	if err != nil {
		if !lenient {
			return nil, fmt.Errorf("%s: %w (use --lenient to fall back to the file's own timestamp)", path, err)
		}
		return fallbackToMtime(f, path)
	}

	bo, ifd0, exifIFD, err := parseTIFF(block)
	if err != nil {
		if !lenient {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return fallbackToMtime(f, path)
	}

	var original, digitized, plain string
	if exifIFD != nil {
		if e, ok := exifIFD[tagDateTimeOriginal]; ok {
			if v, err := e.asciiValue(block, bo); err == nil {
				original = v
			}
		}
		if e, ok := exifIFD[tagDateTimeDigitized]; ok {
			if v, err := e.asciiValue(block, bo); err == nil {
				digitized = v
			}
		}
	}
	if e, ok := ifd0[tagDateTime]; ok {
		if v, err := e.asciiValue(block, bo); err == nil {
			plain = v
		}
	}

	if original == "" {
		if !lenient {
			return nil, fmt.Errorf("%s: no DateTimeOriginal tag present (use --lenient to accept a weaker timestamp)", path)
		}
		if digitized != "" {
			return &CaptureDate{Value: digitized, Source: "DateTimeDigitized"}, nil
		}
		if plain != "" {
			return &CaptureDate{Value: plain, Source: "DateTime"}, nil
		}
		return fallbackToMtime(f, path)
	}

	if !lenient {
		for _, other := range []string{digitized, plain} {
			if other != "" && other != original {
				return nil, fmt.Errorf("%s: timestamp tags disagree (DateTimeOriginal=%q, other tag=%q); rerun with --lenient to accept DateTimeOriginal anyway", path, original, other)
			}
		}
	}

	return &CaptureDate{Value: original, Source: "DateTimeOriginal"}, nil
}

func fallbackToMtime(f *os.File, path string) (*CaptureDate, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: no usable EXIF timestamp and could not stat the file: %w", path, err)
	}
	return &CaptureDate{Value: info.ModTime().Format("2006:01:02 15:04:05"), Source: "file mtime"}, nil
}

// extractEXIFBlock walks a JPEG's marker segments looking for APP1 payloads
// that start with the "Exif\0\0" identifier, and returns the bytes that
// follow it - i.e. the raw TIFF structure that holds the actual tags.
func extractEXIFBlock(f *os.File) ([]byte, error) {
	br := bufio.NewReader(f)

	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil {
		return nil, errors.New("not a JPEG file (could not read SOI marker)")
	}
	if soi[0] != 0xFF || soi[1] != 0xD8 {
		return nil, errors.New("not a JPEG file (missing SOI marker)")
	}

	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, errors.New("no EXIF data found before end of file")
		}
		if b != 0xFF {
			continue
		}

		var marker byte
		for {
			marker, err = br.ReadByte()
			if err != nil {
				return nil, errors.New("no EXIF data found before end of file")
			}
			if marker != 0xFF {
				break
			}
		}

		switch {
		case marker == 0x00 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9):
			// Standalone markers with no length/payload (fill bytes, RST, SOI, EOI).
			if marker == 0xD9 {
				return nil, errors.New("reached end of image without finding an EXIF (APP1) segment")
			}
			continue
		}

		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return nil, fmt.Errorf("truncated segment header at marker 0x%02X", marker)
		}
		segLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		if segLen < 2 {
			return nil, fmt.Errorf("malformed segment length at marker 0x%02X", marker)
		}
		data := make([]byte, segLen-2)
		if _, err := io.ReadFull(br, data); err != nil {
			return nil, fmt.Errorf("truncated segment body at marker 0x%02X", marker)
		}

		if marker == 0xE1 && len(data) >= 6 && string(data[:6]) == "Exif\x00\x00" {
			return data[6:], nil
		}
		if marker == 0xDA {
			// Start of Scan: compressed image data follows and there are no
			// more marker segments worth scanning.
			return nil, errors.New("reached image data without finding an EXIF (APP1) segment")
		}
	}
}

type tiffEntry struct {
	tag           uint16
	typ           uint16
	count         uint32
	valueOrOffset [4]byte
}

// parseTIFF reads the TIFF header at the start of block, then IFD0 and (if
// present) the Exif sub-IFD it points to.
func parseTIFF(block []byte) (bo binary.ByteOrder, ifd0, exifIFD map[uint16]tiffEntry, err error) {
	if len(block) < 8 {
		return nil, nil, nil, errors.New("EXIF block too short to contain a TIFF header")
	}
	switch string(block[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, nil, nil, fmt.Errorf("unrecognized TIFF byte-order marker %q", block[:2])
	}
	if bo.Uint16(block[2:4]) != 0x002A {
		return nil, nil, nil, errors.New("invalid TIFF magic number")
	}

	ifd0Offset := bo.Uint32(block[4:8])
	ifd0, err = readIFD(block, ifd0Offset, bo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading IFD0: %w", err)
	}

	if entry, ok := ifd0[tagExifIFDPointer]; ok {
		if off, err := entry.longValue(bo); err == nil {
			// A missing or malformed sub-IFD is not fatal on its own; the
			// caller just won't find DateTimeOriginal/DateTimeDigitized.
			exifIFD, _ = readIFD(block, off, bo)
		}
	}

	return bo, ifd0, exifIFD, nil
}

func readIFD(block []byte, offset uint32, bo binary.ByteOrder) (map[uint16]tiffEntry, error) {
	if int64(offset)+2 > int64(len(block)) {
		return nil, errors.New("IFD offset out of range")
	}
	count := bo.Uint16(block[offset:])
	entries := make(map[uint16]tiffEntry, count)
	pos := int64(offset) + 2
	for i := 0; i < int(count); i++ {
		if pos+12 > int64(len(block)) {
			return nil, errors.New("IFD entry runs past end of block")
		}
		e := tiffEntry{
			tag:   bo.Uint16(block[pos:]),
			typ:   bo.Uint16(block[pos+2:]),
			count: bo.Uint32(block[pos+4:]),
		}
		copy(e.valueOrOffset[:], block[pos+8:pos+12])
		entries[e.tag] = e
		pos += 12
	}
	return entries, nil
}

func (e tiffEntry) asciiValue(block []byte, bo binary.ByteOrder) (string, error) {
	if e.typ != tiffTypeASCII {
		return "", fmt.Errorf("tag 0x%04X is not an ASCII string (type %d)", e.tag, e.typ)
	}
	size := int64(e.count)
	if size <= 4 {
		return trimASCII(e.valueOrOffset[:size]), nil
	}
	offset := int64(bo.Uint32(e.valueOrOffset[:]))
	if offset < 0 || offset+size > int64(len(block)) {
		return "", fmt.Errorf("tag 0x%04X value out of range", e.tag)
	}
	return trimASCII(block[offset : offset+size]), nil
}

func (e tiffEntry) longValue(bo binary.ByteOrder) (uint32, error) {
	switch e.typ {
	case tiffTypeLong:
		return bo.Uint32(e.valueOrOffset[:]), nil
	case tiffTypeShort:
		return uint32(bo.Uint16(e.valueOrOffset[:2])), nil
	default:
		return 0, fmt.Errorf("tag 0x%04X is not a numeric offset type", e.tag)
	}
}

// trimASCII drops the trailing NUL (and anything after it) that EXIF ASCII
// fields are padded with.
func trimASCII(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
