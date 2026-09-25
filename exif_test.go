package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below build the smallest byte-for-byte valid JPEG/TIFF
// structures that exercise the parser, rather than shipping real photos as
// binary test data.

type tagValue struct {
	tag   uint16
	value string // ASCII value; a trailing NUL is added when it's written out
}

// buildTIFFBlock assembles a minimal little-endian TIFF structure: IFD0
// holding ifd0Fields, and - if exifFields is non-nil - an Exif sub-IFD
// holding exifFields, pointed to by an 0x8769 entry in IFD0.
func buildTIFFBlock(ifd0Fields []tagValue, exifFields []tagValue) []byte {
	bo := binary.LittleEndian
	includeSub := exifFields != nil

	ifd0Count := len(ifd0Fields)
	if includeSub {
		ifd0Count++
	}

	const headerLen = 8
	ifd0Start := headerLen
	ifd0Len := 2 + ifd0Count*12 + 4
	pos := ifd0Start + ifd0Len

	type placed struct {
		tagValue
		offset int
	}
	ifd0Placed := make([]placed, len(ifd0Fields))
	for i, f := range ifd0Fields {
		ifd0Placed[i] = placed{f, pos}
		pos += len(f.value) + 1
	}

	exifIFDStart := pos
	var exifPlaced []placed
	if includeSub {
		exifLen := 2 + len(exifFields)*12 + 4
		pos = exifIFDStart + exifLen
		exifPlaced = make([]placed, len(exifFields))
		for i, f := range exifFields {
			exifPlaced[i] = placed{f, pos}
			pos += len(f.value) + 1
		}
	}

	buf := make([]byte, pos)
	copy(buf[0:2], "II")
	bo.PutUint16(buf[2:4], 0x002A)
	bo.PutUint32(buf[4:8], uint32(ifd0Start))

	bo.PutUint16(buf[ifd0Start:], uint16(ifd0Count))
	epos := ifd0Start + 2
	for _, p := range ifd0Placed {
		writeASCIIEntry(buf, epos, bo, p.tag, p.value, p.offset)
		epos += 12
	}
	if includeSub {
		writeLongEntry(buf, epos, bo, tagExifIFDPointer, uint32(exifIFDStart))
		epos += 12
	}
	bo.PutUint32(buf[epos:], 0) // no next IFD

	if includeSub {
		bo.PutUint16(buf[exifIFDStart:], uint16(len(exifFields)))
		epos = exifIFDStart + 2
		for _, p := range exifPlaced {
			writeASCIIEntry(buf, epos, bo, p.tag, p.value, p.offset)
			epos += 12
		}
		bo.PutUint32(buf[epos:], 0)
	}

	return buf
}

func writeASCIIEntry(buf []byte, epos int, bo binary.ByteOrder, tag uint16, value string, dataOffset int) {
	bo.PutUint16(buf[epos:], tag)
	bo.PutUint16(buf[epos+2:], tiffTypeASCII)
	bo.PutUint32(buf[epos+4:], uint32(len(value)+1))
	bo.PutUint32(buf[epos+8:], uint32(dataOffset))
	copy(buf[dataOffset:], value)
	buf[dataOffset+len(value)] = 0
}

func writeLongEntry(buf []byte, epos int, bo binary.ByteOrder, tag uint16, val uint32) {
	bo.PutUint16(buf[epos:], tag)
	bo.PutUint16(buf[epos+2:], tiffTypeLong)
	bo.PutUint32(buf[epos+4:], 1)
	bo.PutUint32(buf[epos+8:], val)
}

// buildJPEG wraps a TIFF block in a minimal JPEG: an SOI marker, an APP1
// segment carrying the "Exif\0\0" identifier and the TIFF bytes, and an EOI
// marker. Passing a nil tiff produces a JPEG with no EXIF segment at all.
func buildJPEG(tiff []byte) []byte {
	buf := []byte{0xFF, 0xD8}
	if tiff != nil {
		payload := append([]byte("Exif\x00\x00"), tiff...)
		segLen := 2 + len(payload)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(segLen))
		buf = append(buf, 0xFF, 0xE1)
		buf = append(buf, lenBuf[:]...)
		buf = append(buf, payload...)
	}
	buf = append(buf, 0xFF, 0xD9)
	return buf
}

func writeTempFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.jpg")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestReadCaptureDate(t *testing.T) {
	const original = "2024:03:14 08:22:01"
	const digitized = "2024:03:14 08:22:01"
	const digitizedDisagreeing = "2024:03:14 09:00:00"
	const plainDateTime = "2020:01:01 00:00:00"

	cases := []struct {
		name          string
		data          []byte
		lenient       bool
		wantValue     string
		wantSource    string
		wantErrSubstr string
	}{
		{
			name: "strict succeeds when only DateTimeOriginal is present",
			data: buildJPEG(buildTIFFBlock(nil, []tagValue{
				{tagDateTimeOriginal, original},
			})),
			wantValue:  original,
			wantSource: "DateTimeOriginal",
		},
		{
			name: "strict succeeds when other tags agree with DateTimeOriginal",
			data: buildJPEG(buildTIFFBlock(
				[]tagValue{{tagDateTime, original}},
				[]tagValue{
					{tagDateTimeOriginal, original},
					{tagDateTimeDigitized, digitized},
				},
			)),
			wantValue:  original,
			wantSource: "DateTimeOriginal",
		},
		{
			name: "strict fails when DateTimeOriginal is missing",
			data: buildJPEG(buildTIFFBlock(nil, []tagValue{
				{tagDateTimeDigitized, digitized},
			})),
			wantErrSubstr: "no DateTimeOriginal tag present",
		},
		{
			name: "strict fails when tags disagree",
			data: buildJPEG(buildTIFFBlock(nil, []tagValue{
				{tagDateTimeOriginal, original},
				{tagDateTimeDigitized, digitizedDisagreeing},
			})),
			wantErrSubstr: "timestamp tags disagree",
		},
		{
			name:          "strict fails on a JPEG with no EXIF segment",
			data:          buildJPEG(nil),
			wantErrSubstr: "without finding an EXIF",
		},
		{
			name:          "strict fails on a non-JPEG file",
			data:          []byte("not a jpeg at all"),
			wantErrSubstr: "not a JPEG file",
		},
		{
			name: "lenient falls back to DateTimeDigitized",
			data: buildJPEG(buildTIFFBlock(nil, []tagValue{
				{tagDateTimeDigitized, digitized},
			})),
			lenient:    true,
			wantValue:  digitized,
			wantSource: "DateTimeDigitized",
		},
		{
			name: "lenient falls back to plain DateTime",
			data: buildJPEG(buildTIFFBlock(
				[]tagValue{{tagDateTime, plainDateTime}},
				nil,
			)),
			lenient:    true,
			wantValue:  plainDateTime,
			wantSource: "DateTime",
		},
		{
			name:       "lenient falls back to file mtime with no EXIF at all",
			data:       buildJPEG(nil),
			lenient:    true,
			wantSource: "file mtime",
		},
		{
			name: "lenient accepts DateTimeOriginal despite disagreeing tags",
			data: buildJPEG(buildTIFFBlock(nil, []tagValue{
				{tagDateTimeOriginal, original},
				{tagDateTimeDigitized, digitizedDisagreeing},
			})),
			lenient:    true,
			wantValue:  original,
			wantSource: "DateTimeOriginal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, tc.data)
			result, err := ReadCaptureDate(path, tc.lenient)

			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got nil (result: %+v)", tc.wantErrSubstr, result)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErrSubstr, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantValue != "" && result.Value != tc.wantValue {
				t.Errorf("Value = %q, want %q", result.Value, tc.wantValue)
			}
			if result.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", result.Source, tc.wantSource)
			}
		})
	}
}
