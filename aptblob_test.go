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
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gocloud.dev/blob"
	"gocloud.dev/blob/memblob"
	"zombiezen.com/go/aptblob/internal/deb"
)

const testReleaseKey = "dists/stable/Release"

func TestInit(t *testing.T) {
	want := deb.Paragraph{
		{Name: "Origin", Value: "stable"},
		{Name: "Label", Value: "stable"},
		{Name: "Codename", Value: "stable"},
		{Name: "Architectures", Value: "amd64"},
		{Name: "Description", Value: "Some apt repository"},
	}
	ctx := context.Background()
	bucket := memblob.OpenBucket(nil)
	stdin := strings.NewReader(want.String())
	err := cmdInit(ctx, bucket, stdin, ioutil.Discard, "stable", "")
	if err != nil {
		t.Error("init:", err)
	}
	got, _, err := listParagraphs(ctx, bucket, testReleaseKey, deb.ReleaseFields)
	if err != nil {
		t.Error(err)
	}
	ignoreOtherFields := cmpopts.IgnoreSliceElements(func(f deb.Field) bool {
		return want.Get(f.Name) == ""
	})
	if diff := cmp.Diff([]deb.Paragraph{want}, got, sortFields, ignoreOtherFields); diff != "" {
		t.Errorf("%s (-want +got)\n%s", testReleaseKey, diff)
	}
}

func TestUpload(t *testing.T) {
	ctx := context.Background()
	bucket := memblob.OpenBucket(nil)
	err := cmdUpload(ctx, bucket, component{dist: "stable", name: "main"}, "", []string{
		filepath.Join("testdata", "nullpkg_1.0-1.dsc"),
		filepath.Join("testdata", "nullpkg_1.0-1_amd64.deb"),
	})
	if err != nil {
		t.Error("upload:", err)
	}

	const packagesFilename = "main/binary-amd64/Packages"
	const packagesKey = "dists/stable/" + packagesFilename
	gotPackages, packagesData, err := listParagraphs(ctx, bucket, packagesKey, deb.ControlFields)
	if err != nil {
		t.Error(err)
	}
	wantPackages := []deb.Paragraph{
		{
			{Name: "Package", Value: "nullpkg"},
			{Name: "Version", Value: "1.0-1"},
			{Name: "Architecture", Value: "amd64"},
			{Name: "Section", Value: "misc"},
			{Name: "Priority", Value: "optional"},
			{Name: "Maintainer", Value: "Ross Light <ross@zombiezen.com>"},
			{Name: "Description", Value: "Do nothing\n Totally here just to do nothing"},
			{Name: "Installed-Size", Value: "7"},
			{Name: "Size", Value: "1124"},
			{Name: "MD5sum", Value: "bfd9f5f13f02ee8624835719d31ebdb7"},
			{Name: "SHA1", Value: "946b761b186ffcce7e457d6a75355c1450238859"},
			{Name: "SHA256", Value: "6def2db1420e3fc5528a3e1672af87158619ae27d82562c4c155154e68b393b7"},
		},
	}
	ignoreFilename := cmpopts.IgnoreSliceElements(func(f deb.Field) bool {
		return f.Name == "Filename"
	})
	if diff := cmp.Diff(wantPackages, gotPackages, sortFields, ignoreFilename); diff != "" {
		t.Errorf("%s (-want +got):\n%s", packagesKey, diff)
	}
	if len(gotPackages) == 1 {
		file := gotPackages[0].Get("Filename")
		if err := checkFile(ctx, bucket, file, "nullpkg_1.0-1_amd64.deb"); err != nil {
			t.Error(err)
		}
	}

	const sourcesFilename = "main/source/Sources"
	const sourcesKey = "dists/stable/" + sourcesFilename
	gotSources, sourcesData, err := listParagraphs(ctx, bucket, sourcesKey, deb.SourceControlFields)
	if err != nil {
		t.Error(err)
	}
	wantSources := []deb.Paragraph{
		{
			{Name: "Package", Value: "nullpkg"},
			{Name: "Format", Value: "3.0 (quilt)"},
			{Name: "Binary", Value: "nullpkg"},
			{Name: "Architecture", Value: "any"},
			{Name: "Version", Value: "1.0-1"},
			{Name: "Maintainer", Value: "Ross Light <ross@zombiezen.com>"},
			{Name: "Standards-Version", Value: "3.9.2"},
			{Name: "Build-Depends", Value: "debhelper (>= 11)"},
			{Name: "Package-List", Value: "\n nullpkg deb misc optional arch=any"},
			{
				Name: "Checksums-Sha1",
				Value: "\n e77b8cd6a21289abd88d8195273a692b7a67cab5 121 nullpkg_1.0.orig.tar.gz" +
					"\n f949ed3c2b430378c6ba676a21c986c953c90521 640 nullpkg_1.0-1.debian.tar.xz",
			},
			{
				Name: "Checksums-Sha256",
				Value: "\n 5bf21f0c62248b89a88ef2f00296ef744c29e589dfb53e50eba55e928ad06e0c 121 nullpkg_1.0.orig.tar.gz" +
					"\n 35dc7a93ca59195bc7a897e785aaaba00935943a16815bdb263f348da3042bf5 640 nullpkg_1.0-1.debian.tar.xz",
			},
			{
				Name: "Files",
				Value: "\n 553a0f3ac9d12929fe08c01c3211fdb1 121 nullpkg_1.0.orig.tar.gz" +
					"\n bbd67ff58e0e03cc699ad83778e87081 640 nullpkg_1.0-1.debian.tar.xz",
			},
		},
	}
	ignoreDir := cmpopts.IgnoreSliceElements(func(f deb.Field) bool {
		return f.Name == "Directory"
	})
	if diff := cmp.Diff(wantSources, gotSources, sortFields, ignoreDir); diff != "" {
		t.Errorf("%s (-want +got):\n%s", sourcesKey, diff)
	}
	if len(gotSources) == 1 {
		dir := gotSources[0].Get("Directory")
		if err := checkFile(ctx, bucket, dir+"/nullpkg_1.0.orig.tar.gz", "nullpkg_1.0.orig.tar.gz"); err != nil {
			t.Error(err)
		}
		if err := checkFile(ctx, bucket, dir+"/nullpkg_1.0-1.debian.tar.xz", "nullpkg_1.0-1.debian.tar.xz"); err != nil {
			t.Error(err)
		}
	}

	releaseData, err := bucket.ReadAll(ctx, testReleaseKey)
	release, err := deb.ParseReleaseIndex(bytes.NewReader(releaseData))
	if err != nil {
		t.Error(err)
	}
	ignoreOtherFiles := cmpopts.IgnoreSliceElements(func(sig deb.IndexSignature) bool {
		return sig.Filename != packagesFilename && sig.Filename != sourcesFilename
	})
	signatureTests := []struct {
		fieldName string
		newHash   func() hash.Hash
	}{
		{"MD5Sum", md5.New},
		{"SHA1", sha1.New},
		{"SHA256", sha256.New},
	}
	for _, test := range signatureTests {
		want := []deb.IndexSignature{
			newIndexSignature(test.newHash(), sourcesData, sourcesFilename),
			newIndexSignature(test.newHash(), packagesData, packagesFilename),
		}
		got, err := deb.ParseIndexSignatures(release.Get(test.fieldName), test.newHash().Size())
		if err != nil {
			t.Errorf("%s: %v", test.fieldName, err)
			continue
		}
		if diff := cmp.Diff(want, got, sortSignatures, ignoreOtherFiles); diff != "" {
			t.Errorf("%s (-want +got):\n%s", test.fieldName, diff)
		}
	}
}

func listParagraphs(ctx context.Context, b *blob.Bucket, key string, fields map[string]deb.FieldType) ([]deb.Paragraph, []byte, error) {
	r, err := b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	buf := new(bytes.Buffer)
	tr := io.TeeReader(r, buf)
	p := deb.NewParser(tr)
	p.Fields = fields
	var got []deb.Paragraph
	for p.Next() {
		got = append(got, append(deb.Paragraph(nil), p.Paragraph()...))
	}
	if err := p.Err(); err != nil {
		return got, buf.Bytes(), fmt.Errorf("%s: %w", key, err)
	}
	return got, buf.Bytes(), nil
}

func checkFile(ctx context.Context, b *blob.Bucket, key string, testdataPath string) error {
	got, err := b.ReadAll(ctx, key)
	if err != nil {
		return err
	}
	want, err := ioutil.ReadFile(filepath.Join("testdata", filepath.FromSlash(testdataPath)))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s does not match testdata/%s", key, testdataPath)
	}
	return nil
}

var sortFields = cmpopts.SortSlices(func(f1, f2 deb.Field) bool {
	return f1.Name < f2.Name
})

func newIndexSignature(h hash.Hash, data []byte, name string) deb.IndexSignature {
	h.Write(data)
	return deb.IndexSignature{
		Filename: name,
		Size:     int64(len(data)),
		Checksum: h.Sum(nil),
	}
}

var sortSignatures = cmpopts.SortSlices(func(sig1, sig2 deb.IndexSignature) bool {
	return sig1.Filename < sig2.Filename
})

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
