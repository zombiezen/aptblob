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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A Parser reads fields from a control file.
// The syntax is documented at https://www.debian.org/doc/debian-policy/ch-controlfields.html#syntax-of-control-files
type Parser struct {
	// Fields specifies the type of possible fields.
	Fields map[string]FieldType

	scan   *bufio.Scanner
	lineno int
	para   Paragraph
	err    error
}

// NewParser returns a new parser that reads from r.
func NewParser(r io.Reader) *Parser {
	p := &Parser{
		scan:   bufio.NewScanner(r),
		lineno: 1,
	}
	// Split by paragraph.
	p.scan.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		for advance < len(data) {
			start := advance
			var line []byte
			if i := bytes.IndexByte(data[advance:], '\n'); i != -1 {
				line = data[advance : advance+i]
				advance += i + 1
			} else if atEOF {
				line = data[advance:]
				advance = len(data)
			} else {
				// Not enough buffered for complete line.
				return 0, nil, nil
			}
			if isEmptyLine(line) {
				if token == nil {
					// Advance lineno for leading empty lines.
					p.lineno++
				}
				return
			}
			token = data[:start+len(line)]
		}
		if !atEOF {
			// Not enough buffered.
			return 0, nil, nil
		}
		return
	})
	return p
}

// Single parses a single-paragraph control file, which will then be available
// through the Paragraph method. It returns false if the method is called after
// any call to Next, the parser stops before reading a paragraph, or the parser
// encounters a syntax error. If Single returns false, Err() will always return
// an error.
func (p *Parser) Single() bool {
	if p.err != nil {
		return false
	}
	if p.lineno != 1 {
		p.clear()
		p.err = errors.New("parse debian control file: Parser.Single called after Parser.Next")
		return false
	}
	if !p.Next() {
		if p.err == nil {
			p.err = fmt.Errorf("parse debian control file: %w", io.ErrUnexpectedEOF)
		}
		return false
	}

	// Check for trailing data.
	if p.scan.Scan() {
		p.clear()
		p.err = fmt.Errorf("parse debian control file: line %d: multiple paragraphs encountered", p.lineno)
		return false
	}
	if err := p.scan.Err(); err != nil {
		p.clear()
		p.err = fmt.Errorf("parse debian control file: line %d: %w", p.lineno, err)
		return false
	}
	return true
}

// Next advances the parser to the next paragraph, which will then be available
// through the Paragraph method. It returns false when the parser stops, either
// by reaching the end of input or an error.
func (p *Parser) Next() bool {
	if p.err != nil {
		return false
	}
	p.clear()
	if !p.scan.Scan() {
		if err := p.scan.Err(); err != nil {
			p.err = fmt.Errorf("parse debian control file: line %d: %w", p.lineno, err)
		}
		return false
	}
	text := p.scan.Text()
	for len(text) > 0 {
		valueEnd := len(text)
		if i := strings.IndexByte(text, '\n'); i != -1 {
			// Always i > 0, since paragraph separators are scanned out.
			valueEnd = i
		}
		if text[0] == '#' {
			p.clear()
			p.err = fmt.Errorf("parse debian control file: line %d: comments not allowed", p.lineno)
			return false
		}

		// Parse field name.
		colon := strings.IndexByte(text[:valueEnd], ':')
		if colon == -1 {
			p.clear()
			p.err = fmt.Errorf("parse debian control file: line %d: missing colon", p.lineno)
			return false
		}
		field := Field{Name: text[:colon]}
		if err := validateFieldName(field.Name); err != nil {
			p.clear()
			p.err = fmt.Errorf("parse debian control file: line %d: %w", p.lineno, err)
			return false
		}
		if p.para.find(field.Name) != -1 {
			p.clear()
			p.err = fmt.Errorf("parse debian control file: line %d: multiple fields for %q", p.lineno, field.Name)
			return false
		}

		// Locate end of field value, considering any continuation lines.
		startLine := p.lineno
		for valueEnd+1 < len(text) && strings.IndexByte(" \t#", text[valueEnd+1]) != -1 {
			p.lineno++
			if text[valueEnd+1] == '#' {
				p.clear()
				p.err = fmt.Errorf("parse debian control file: line %d: comments not allowed", p.lineno)
				return false
			}
			i := strings.IndexByte(text[valueEnd+1:], '\n')
			if i == -1 {
				valueEnd = len(text)
			} else {
				valueEnd += 1 + i
			}
		}
		switch p.Fields[field.Name] {
		case Simple:
			if p.lineno != startLine {
				p.clear()
				p.err = fmt.Errorf("parse debian control file: line %d: field %q must be a single line", startLine, field.Name)
				return false

			}
			field.Value = text[colon+1 : valueEnd]
		case Folded:
			field.Value = strings.ReplaceAll(text[colon+1:valueEnd], "\n", "")
		case Multiline:
			field.Value = text[colon+1 : valueEnd]
		default:
			panic("unknown field type")
		}
		field.Value = strings.Trim(field.Value, " \t")
		if field.Value == "" {
			p.clear()
			p.err = fmt.Errorf("parse debian control file: line %d: empty field %q", startLine, field.Name)
			return false
		}

		// Add field to paragraph and advance to following line.
		p.para = append(p.para, field)
		text = strings.TrimPrefix(text[valueEnd:], "\n")
		p.lineno++
	}
	return true
}

func validateFieldName(name string) error {
	if name == "" {
		return errors.New("empty field name")
	}
	if name[0] == '-' {
		return fmt.Errorf("field name %q begins with hyphen", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !('!' <= c && c <= '9' || ';' <= c && c <= '~') {
			return fmt.Errorf("field name %q has forbidden character %q", name, c)
		}
	}
	return nil
}

// FieldType is an enumeration of the types of fields.
type FieldType int

const (
	// Simple indicates a single-line field.
	Simple FieldType = iota
	// Multiline indicates a field that may contain multiple lines.
	Multiline
	// Folded indicates a field that may span multiple lines, but newlines are
	// stripped before being returned.
	Folded
)

func (p *Parser) clear() {
	for i := range p.para {
		p.para[i] = Field{}
	}
	p.para = p.para[:0]
}

func (p *Parser) Paragraph() Paragraph {
	return p.para[:len(p.para):len(p.para)]
}

func (p *Parser) Err() error {
	return p.err
}

// Field is a single field in a control file.
type Field struct {
	Name  string
	Value string
}

// String formats the field as a line in a "Release" file.
func (f Field) String() string {
	sb := new(strings.Builder)
	f.write(sb)
	return sb.String()
}

var fieldSep = []byte(": ")

func (f Field) write(w io.Writer) error {
	if _, err := io.WriteString(w, f.Name); err != nil {
		return err
	}
	val := strings.Trim(f.Value, " \t")
	sepN := len(fieldSep)
	if strings.HasPrefix(val, "\n") {
		sepN = 1
	}
	if _, err := w.Write(fieldSep[:sepN]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, val); err != nil {
		return err
	}
	return nil
}

// Paragraph is an ordered mapping of fields in a control file.
type Paragraph []Field

var newlines = []byte{'\n', '\n'}

// Save writes a list of paragraphs to a writer.
func Save(w io.Writer, paragraphs []Paragraph) error {
	if len(paragraphs) == 0 {
		return nil
	}
	for i, p := range paragraphs {
		if i > 0 {
			if _, err := w.Write(newlines); err != nil {
				return fmt.Errorf("save debian control file: %w", err)
			}
		}
		if err := p.write(w); err != nil {
			return fmt.Errorf("save debian control file: %w", err)
		}
	}
	if _, err := w.Write(newlines[:1]); err != nil {
		return fmt.Errorf("save debian control file: %w", err)
	}
	return nil
}

func (m Paragraph) find(name string) int {
	for i, f := range m {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// Get returns the value of the field with the given name or the empty string
// if the field is not present in the paragraph.
func (para Paragraph) Get(name string) string {
	i := para.find(name)
	if i == -1 {
		return ""
	}
	return para[i].Value
}

// Set sets the value of the named field, appending it to the paragraph if necessary.
func (para *Paragraph) Set(name, value string) {
	i := para.find(name)
	if i == -1 {
		*para = append(*para, Field{name, value})
		return
	}
	(*para)[i].Value = value
}

// String formats the fields as lines in a "Release" file.
func (m Paragraph) String() string {
	sb := new(strings.Builder)
	m.write(sb)
	return sb.String()
}

func (m Paragraph) write(w io.Writer) error {
	for i, f := range m {
		if i > 0 {
			if _, err := w.Write(newlines[:1]); err != nil {
				return err
			}
		}
		if err := f.write(w); err != nil {
			return err
		}
	}
	return nil
}

func isEmptyLine(line []byte) bool {
	for _, b := range line {
		if b != ' ' && b != '\t' && b != '\n' {
			return false
		}
	}
	return true
}
