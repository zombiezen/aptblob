package deb

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	slashpath "path"

	"github.com/laher/argo/ar"
	"github.com/ulikunitz/xz"
)

// ExtractControl reads the control file from a binary package.
func ExtractControl(r io.Reader) ([]byte, error) {
	arr, err := ar.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("extract deb control: %w", err)
	}

	hdr, err := arr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("extract deb control: %w", err)
	}
	if hdr.Name != "debian-binary" {
		return nil, fmt.Errorf("extract deb control: unknown format")
	}
	format, err := ioutil.ReadAll(arr)
	if err != nil {
		return nil, fmt.Errorf("extract deb control: %w", err)
	}
	if string(format) != "2.0\n" {
		return nil, fmt.Errorf("extract deb control: unknown format %q", format)
	}

	hdr, err = arr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("extract deb control: %w", err)
	}
	var controlReader io.ReadCloser
	switch hdr.Name {
	case "control.tar":
		controlReader = ioutil.NopCloser(arr)
	case "control.tar.gz":
		var err error
		controlReader, err = gzip.NewReader(arr)
		if err != nil {
			return nil, fmt.Errorf("extract deb control: control.tar.gz: %w", err)
		}
	case "control.tar.xz":
		xzr, err := xz.NewReader(arr)
		if err != nil {
			return nil, fmt.Errorf("extract deb control: control.tar.xz: %w", err)
		}
		controlReader = ioutil.NopCloser(xzr)
	default:
		return nil, fmt.Errorf("extract deb control: unexpected member %q", hdr.Name)
	}
	controlArchiveName := hdr.Name
	defer controlReader.Close()

	tarr := tar.NewReader(controlReader)
	for {
		hdr, err := tarr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("extract deb control: %s: does not contain \"control\"", controlArchiveName)
			}
			return nil, fmt.Errorf("extract deb control: %s: %w", controlArchiveName, err)
		}
		name := slashpath.Clean(hdr.Name)
		if name == "control" {
			data, err := ioutil.ReadAll(tarr)
			if err != nil {
				return nil, fmt.Errorf("extract deb control: %s: control: %w", controlArchiveName, err)
			}
			return data, nil
		}
	}
}

// ControlFields is the set of fields in the binary package control file.
var ControlFields = map[string]FieldType{
	"Binary":           Folded,
	"Changes":          Multiline,
	"Checksums-Sha1":   Multiline,
	"Checksums-Sha256": Multiline,
	"Description":      Multiline,
	"Dgit":             Folded,
	"Files":            Multiline,
	"Package-List":     Multiline,
	"Uploaders":        Folded,
}
