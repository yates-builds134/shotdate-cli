// Command shotdate prints the capture timestamp a JPEG's EXIF data claims
// for itself.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	lenient := flag.Bool("lenient", false, "accept a weaker timestamp (DateTimeDigitized, DateTime, or file mtime) "+
		"when DateTimeOriginal is missing or the timestamp tags disagree")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "shotdate: print the capture timestamp an image's EXIF data claims\n\n")
		fmt.Fprintf(os.Stderr, "usage: %s [--lenient] <image.jpg> [image.jpg ...]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	exitCode := 0
	multi := len(args) > 1
	for _, path := range args {
		result, err := ReadCaptureDate(path, *lenient)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			exitCode = 1
			continue
		}
		if multi {
			fmt.Printf("%s\t%s\t(%s)\n", path, result.Value, result.Source)
		} else {
			fmt.Printf("%s\t(%s)\n", result.Value, result.Source)
		}
	}
	os.Exit(exitCode)
}
