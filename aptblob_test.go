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

package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"zombiezen.com/go/aptblob/internal/deb"
)

func TestDedupePackages(t *testing.T) {
	tests := []struct {
		name      string
		packages  []deb.Paragraph
		want      []deb.Paragraph
		wantError bool
	}{
		{
			name: "Empty",
			want: nil,
		},
		{
			name: "SinglePackage",
			packages: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
				},
			},
			want: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
				},
			},
		},
		{
			name: "UnrelatedPackages",
			packages: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
				},
				{
					{Name: "Package", Value: "git"},
					{Name: "Version", Value: "2.20"},
				},
			},
			want: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
				},
				{
					{Name: "Package", Value: "git"},
					{Name: "Version", Value: "2.20"},
				},
			},
		},
		{
			name: "DifferentVersionsOfSamePackage",
			packages: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
					{Name: "Foo", Value: "bar"},
				},
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.2"},
					{Name: "Baz", Value: "quux"},
				},
			},
			want: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
					{Name: "Foo", Value: "bar"},
				},
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.2"},
					{Name: "Baz", Value: "quux"},
				},
			},
		},
		{
			name: "SamePackageAndVersion",
			packages: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
					{Name: "Foo", Value: "bar"},
				},
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
					{Name: "Baz", Value: "quux"},
				},
			},
			want: []deb.Paragraph{
				{
					{Name: "Package", Value: "libc6"},
					{Name: "Version", Value: "6.1"},
					{Name: "Baz", Value: "quux"},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dedupePackages(test.packages)
			if err != nil {
				t.Log("dedupePackages:", err)
				if !test.wantError {
					t.Fail()
				}
				return
			}
			if test.wantError {
				t.Fatalf("packages = %v; want error", got)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("packages (-want +got):\n%s", diff)
			}
		})
	}
}
