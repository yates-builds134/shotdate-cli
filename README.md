# shotdate

`shotdate` answers one question: **what capture date does this image's EXIF
data actually claim?**

Most tools that print "the date" of a photo quietly pick whichever timestamp
they find first - EXIF DateTimeOriginal, DateTime, file mtime, whatever - and
never tell you which one it was. That's fine until you're deduplicating a
photo library, backdating an import, or building a timeline, and half your
files have been re-saved by some app that rewrote DateTime but left
DateTimeOriginal alone. Silently picking a timestamp means silently getting
some of them wrong.

`shotdate` is strict by default:

- it only trusts `DateTimeOriginal` (the tag a camera sets when the shutter
  fires, not when the file happens to be written)
- if that tag is missing, it fails instead of guessing
- if other timestamp tags in the file disagree with `DateTimeOriginal`, it
  fails and tells you what disagreed, instead of picking one

If you want it to be more forgiving - falling back to `DateTimeDigitized`,
then the plain `DateTime` tag, then finally the file's own mtime - pass
`--lenient`. Nothing happens quietly; the source of the timestamp is always
printed alongside it so you know exactly how much to trust it.

## usage

```
$ shotdate photo.jpg
2024:03:14 08:22:01	(DateTimeOriginal)

$ shotdate screenshot.png
screenshot.png: not a JPEG file (missing SOI marker) (use --lenient to fall back to the file's own timestamp)

$ shotdate --lenient screenshot.png
2024:03:14 09:01:47	(file mtime)

$ shotdate edited.jpg
edited.jpg: timestamp tags disagree (DateTimeOriginal="2019:07:02 14:10:00", other tag="2024:03:14 09:00:00"); rerun with --lenient to accept DateTimeOriginal anyway

$ shotdate a.jpg b.jpg c.jpg
a.jpg	2024:03:14 08:22:01	(DateTimeOriginal)
b.jpg: no DateTimeOriginal tag present (use --lenient to accept a weaker timestamp)
c.jpg	2021:11:30 17:05:12	(DateTimeOriginal)
```

Exit code is 0 if every file produced a timestamp, 1 if any file failed, 2 if
called with no arguments.

## how it works

JPEGs store EXIF metadata in an `APP1` marker segment near the start of the
file. That segment holds a small TIFF structure (its own byte order, its own
tag/offset format) with an `IFD0` that in turn points at an `Exif` sub-IFD
where the interesting timestamp tags live. `shotdate` walks the JPEG's marker
segments to find that block, then parses just enough of the TIFF structure to
pull out three tags: `DateTimeOriginal` (0x9003), `DateTimeDigitized`
(0x9004), and `DateTime` (0x0132). No image is decoded and no dependency is
pulled in to do it - it's a few hundred lines of byte-offset arithmetic
against the standard library.

## current limitations

- JPEG only. TIFF, HEIC, and raw formats store EXIF differently and aren't
  read yet.
- Timestamps are printed in their raw EXIF form (`YYYY:MM:DD HH:MM:SS`, no
  timezone - EXIF mostly doesn't have one). No parsing into `time.Time` yet.
- No JSON output mode.

## building

Standard library only, no external dependencies:

```
go build .
```

## license

MIT, see LICENSE.
