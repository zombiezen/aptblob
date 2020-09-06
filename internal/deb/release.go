// Copyright 2020 Ross Light
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package deb

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
	p.Fields = ReleaseFields
	if !p.Single() {
		return nil, fmt.Errorf("parse Release: %w", p.Err())
	}
	return p.Paragraph(), nil
}

// ReleaseFields is the set of fields in the release information file.
var ReleaseFields = map[string]FieldType{
	"MD5Sum": Multiline,
	"SHA1":   Multiline,
	"SHA256": Multiline,
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
