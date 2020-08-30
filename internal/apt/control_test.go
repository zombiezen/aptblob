package apt

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParser(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		fields  map[string]FieldType
		want    []Paragraph
		wantErr bool
	}{
		{
			name: "Empty",
		},
		{
			name:   "SingleField",
			source: "Package: libc6\n",
			want: []Paragraph{
				{{Name: "Package", Value: "libc6"}},
			},
		},
		{
			name:   "MissingLF",
			source: "Package: libc6",
			want: []Paragraph{
				{{Name: "Package", Value: "libc6"}},
			},
		},
		{
			name:   "FieldNameContainsHash",
			source: "Foo#Bar: libc6\n",
			want: []Paragraph{
				{{Name: "Foo#Bar", Value: "libc6"}},
			},
		},
		{
			name:   "MultipleFields",
			source: "Package: libc6\nArchitecture: any\n",
			want: []Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Architecture", Value: "any"},
				},
			},
		},
		{
			name:   "MultipleParagraphs",
			source: "Package: libc6\nArchitecture: any\n\nPackage: git\n",
			want: []Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Architecture", Value: "any"},
				},
				{
					{Name: "Package", Value: "git"},
				},
			},
		},
		{
			name:   "FoldedField",
			source: "Depends: ${misc:Depends},\n  ${shlibs:Depends},\n  libc6\n",
			fields: map[string]FieldType{"Depends": Folded},
			want: []Paragraph{
				{{Name: "Depends", Value: "${misc:Depends},  ${shlibs:Depends},  libc6"}},
			},
		},
		{
			name:   "MultilineField/SingleLine",
			source: "Description: Hello World\n",
			fields: map[string]FieldType{"Description": Multiline},
			want: []Paragraph{
				{{Name: "Description", Value: " Hello World"}},
			},
		},
		{
			name:   "MultilineField/MultipleLines",
			source: "Description: Hello World\n Extended description\n",
			fields: map[string]FieldType{"Description": Multiline},
			want: []Paragraph{
				{{Name: "Description", Value: " Hello World\n Extended description"}},
			},
		},
		{
			name:   "MultilineField/NoFirstLine",
			source: "Description:\n Extended description\n",
			fields: map[string]FieldType{"Description": Multiline},
			want: []Paragraph{
				{{Name: "Description", Value: "\n Extended description"}},
			},
		},
		{
			name:   "MultilineField/TrailingWhitespaceFirstLine",
			source: "Description: \n Extended description\n",
			fields: map[string]FieldType{"Description": Multiline},
			want: []Paragraph{
				{{Name: "Description", Value: " \n Extended description"}},
			},
		},

		// Comments
		{
			name:    "Comment/Leading",
			source:  "# Foo\nPackage: libc6\n",
			wantErr: true,
		},
		{
			name:    "Comment/Inner",
			source:  "Package: libc6\n# Foo\nArchitecture: any\n",
			wantErr: true,
		},
		{
			name:    "Comment/DuringContinuation",
			source:  "Description: Hello\n# Foo\n World!\n",
			fields:  map[string]FieldType{"Description": Multiline},
			wantErr: true,
		},
		{
			name:    "Comment/Trailing",
			source:  "Package: libc6\nArchitecture: any\n# Foo\n",
			wantErr: true,
		},

		// Errors
		{
			name:    "MultipleFieldsCollide",
			source:  "Package: libc6\nPackage: libc6\n",
			wantErr: true,
		},
		{
			name:    "FieldNameStartsWithHyphen",
			source:  "-Package: libc6\n",
			wantErr: true,
		},
		{
			name:    "FieldNameMustBePresent",
			source:  ": libc6\n",
			wantErr: true,
		},
		{
			name:    "FieldValueMustBePresent",
			source:  "Package: \n",
			wantErr: true,
		},
		{
			name:    "ContinuationOnSimpleField",
			source:  "Package: libc6\n more\n",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := NewParser(strings.NewReader(test.source))
			p.Fields = test.fields
			var got []Paragraph
			for p.Next() && len(got) < len(test.want)+10 {
				got = append(got, append(Paragraph(nil), p.Paragraph()...))
			}
			if err := p.Err(); err != nil {
				t.Log("Err() =", err)
				if !test.wantErr {
					t.Fail()
				}
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("paragraphs (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("Single", func(t *testing.T) {
		for _, test := range tests {
			if test.wantErr || len(test.want) != 1 {
				continue
			}
			t.Run(test.name, func(t *testing.T) {
				p := NewParser(strings.NewReader(test.source))
				p.Fields = test.fields
				if !p.Single() {
					t.Error("Single() returned false")
				}
				if err := p.Err(); err != nil {
					t.Error("Err() =", err)
				}
				got := p.Paragraph()
				if diff := cmp.Diff(test.want[0], got); diff != "" {
					t.Errorf("paragraphs (-want +got):\n%s", diff)
				}
			})
		}
	})
}
