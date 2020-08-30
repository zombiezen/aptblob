package apt

import (
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseReleaseIndex parses a "Release" file.
// https://wiki.debian.org/DebianRepository/Format#A.22Release.22_files
func ParseReleaseIndex(r io.Reader) (Paragraph, error) {
	p := NewParser(r)
	p.Fields = map[string]FieldType{
		"MD5Sum": Multiline,
		"SHA1":   Multiline,
		"SHA256": Multiline,
	}
	if !p.Single() {
		err := p.Err()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("parse Release: %w", err)
	}
	return p.Paragraph(), nil
}

type IndexSignature struct {
	Checksum []byte
	Size     int64
	Filename string
}

func ParseIndexSignatures(fieldValue string, checksumSize int) ([]IndexSignature, error) {
	lines := strings.Split(fieldValue, "\n")
	sigs := make([]IndexSignature, 0, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sig, err := parseIndexSignature(line, checksumSize)
		if err != nil {
			return nil, fmt.Errorf("signature #%d: %w", i+1, err)
		}
		sigs = append(sigs, sig)
	}
	return sigs, nil
}

func parseIndexSignature(line string, checksumSize int) (IndexSignature, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return IndexSignature{}, fmt.Errorf("parse signature: line has %d fields", len(fields))
	}
	var sig IndexSignature
	if got, want := len(fields[0]), hex.EncodedLen(checksumSize); got != want {
		return IndexSignature{}, fmt.Errorf("parse signature: checksum: size %d (expected %d)", got, want)
	}
	var err error
	sig.Checksum, err = hex.DecodeString(fields[0])
	if err != nil {
		return IndexSignature{}, fmt.Errorf("parse signature: checksum: %w", err)
	}
	sig.Size, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return IndexSignature{}, fmt.Errorf("parse signature: size: %w", err)
	}
	sig.Filename = string(fields[2])
	return sig, nil
}

func (sig IndexSignature) String() string {
	return fmt.Sprintf("%x %d %s", sig.Checksum, sig.Size, sig.Filename)
}
