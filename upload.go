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
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"os"
	"os/exec"
	slashpath "path"
	"path/filepath"
	"strconv"
	"strings"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/crypto/openpgp/clearsign"
	"zombiezen.com/go/aptblob/internal/deb"
)

type distribution string

func (dist distribution) dir() string {
	return "dists/" + string(dist)
}

func (dist distribution) indexPath() string {
	return dist.dir() + "/Release"
}

func (dist distribution) signedIndexPath() string {
	return dist.dir() + "/InRelease"
}

func (dist distribution) indexSignaturePath() string {
	return dist.dir() + "/Release.gpg"
}

type component struct {
	dist distribution
	name string
}

func (comp component) dir() string {
	return comp.dist.dir() + "/" + comp.name
}

func (comp component) binaryIndexPath(arch string) string {
	return comp.dir() + "/binary-" + arch + "/Packages"
}

func (comp component) sourceIndexPath() string {
	return comp.dir() + "/source/Sources"
}

func uploadReleaseIndex(ctx context.Context, bucket *blob.Bucket, dist distribution, release deb.Paragraph, keyID string) error {
	data := new(bytes.Buffer)
	deb.Save(data, []deb.Paragraph{release})
	err := bucket.WriteAll(ctx, dist.indexPath(), data.Bytes(), &blob.WriterOptions{
		ContentType: "text/plain; charset=utf-8",
	})
	if err != nil {
		return fmt.Errorf("upload Release: %w", err)
	}

	if keyID == "" {
		return nil
	}

	clearSign := exec.CommandContext(ctx, "gpg", "-a", "-u", keyID+"!", "--clear-sign")
	clearSign.Stdin = bytes.NewReader(data.Bytes())
	clearSignOutput := new(bytes.Buffer)
	clearSign.Stdout = clearSignOutput
	clearSign.Stderr = os.Stderr
	if err := clearSign.Run(); err != nil {
		return fmt.Errorf("generate InRelease: %w", err)
	}
	err = bucket.WriteAll(ctx, dist.signedIndexPath(), clearSignOutput.Bytes(), &blob.WriterOptions{
		ContentType: "text/plain; charset=utf-8",
	})
	if err != nil {
		return fmt.Errorf("upload InRelease: %w", err)
	}

	detachSign := exec.CommandContext(ctx, "gpg", "-a", "-u", keyID+"!", "--detach-sign")
	detachSign.Stdin = bytes.NewReader(data.Bytes())
	detachSignOutput := new(bytes.Buffer)
	detachSign.Stdout = detachSignOutput
	detachSign.Stderr = os.Stderr
	if err := detachSign.Run(); err != nil {
		return fmt.Errorf("generate Release.gpg: %w", err)
	}
	err = bucket.WriteAll(ctx, dist.indexSignaturePath(), detachSignOutput.Bytes(), &blob.WriterOptions{
		ContentType: "text/plain; charset=utf-8",
	})
	if err != nil {
		return fmt.Errorf("upload Release.gpg: %w", err)
	}
	return nil
}

type indexHashes struct {
	size   int64
	md5    [md5.Size]byte
	sha1   [sha1.Size]byte
	sha256 [sha256.Size]byte
}

const gzipExtension = ".gz"

func uploadIndex(ctx context.Context, bucket *blob.Bucket, key string, packages []deb.Paragraph) (uncompressed, gzipped indexHashes, err error) {
	buf := new(bytes.Buffer)
	if err := deb.Save(buf, packages); err != nil {
		return indexHashes{}, indexHashes{}, err
	}
	uncompressed, err = upload(ctx, bucket, key, bytes.NewReader(buf.Bytes()), uploadOptions{
		contentType: "text/plain; charset=utf-8",
	})
	if err != nil {
		return indexHashes{}, indexHashes{}, err
	}
	gzipBuf := new(bytes.Buffer)
	zw := gzip.NewWriter(gzipBuf)
	if _, err := io.Copy(zw, buf); err != nil {
		return indexHashes{}, indexHashes{}, fmt.Errorf("compress %s: %w", key, err)
	}
	if err := zw.Close(); err != nil {
		return indexHashes{}, indexHashes{}, fmt.Errorf("compress %s: %w", key, err)
	}
	gzipped, err = upload(ctx, bucket, key+gzipExtension, bytes.NewReader(gzipBuf.Bytes()), uploadOptions{
		contentType: "application/gzip",
	})
	if err != nil {
		return indexHashes{}, indexHashes{}, err
	}
	return
}

func uploadBinaryPackage(ctx context.Context, bucket *blob.Bucket, debPath string) (deb.Paragraph, error) {
	debName := filepath.Base(debPath)
	debFile, err := os.Open(debPath)
	if err != nil {
		return nil, fmt.Errorf("upload binary package %s: %w", debName, err)
	}
	defer debFile.Close()
	control, err := deb.ExtractControl(debFile)
	if err != nil {
		return nil, fmt.Errorf("upload binary package %s: %w", debName, err)
	}
	p := deb.NewParser(bytes.NewReader(control))
	p.Fields = deb.ControlFields
	if !p.Single() {
		if err := p.Err(); err != nil {
			return nil, fmt.Errorf("upload binary package %s: control: %w", debName, err)
		}
		return nil, fmt.Errorf("upload binary package %s: control: empty file", debName)
	}
	pkg := p.Paragraph()
	promotePackageField(pkg)
	arch := pkg.Get("Architecture")
	if arch == "" {
		return nil, fmt.Errorf("upload binary package %s: missing Architecture field", debName)
	}
	packageHashes, err := upload(ctx, bucket, poolPath(debName), debFile, uploadOptions{
		contentType:  "application/vnd.debian.binary-package",
		cacheControl: immutable,
	})
	if err != nil {
		return nil, fmt.Errorf("upload binary package %s: %w", debName, err)
	}
	pkg.Set("Filename", poolPath(debName))
	pkg.Set("Size", strconv.FormatInt(packageHashes.size, 10))
	pkg.Set("MD5sum", hex.EncodeToString(packageHashes.md5[:]))
	pkg.Set("SHA1", hex.EncodeToString(packageHashes.sha1[:]))
	pkg.Set("SHA256", hex.EncodeToString(packageHashes.sha256[:]))
	return pkg, nil
}

func uploadSourcePackage(ctx context.Context, bucket *blob.Bucket, dscPath string) (deb.Paragraph, error) {
	packageName := strings.TrimSuffix(filepath.Base(dscPath), ".dsc")
	dsc, err := ioutil.ReadFile(dscPath)
	if err != nil {
		return nil, fmt.Errorf("upload source package %s: %w", packageName, err)
	}
	p := deb.NewParser(bytes.NewReader(maybeClearSigned(dsc)))
	p.Fields = deb.SourceControlFields
	if !p.Single() {
		if err := p.Err(); err != nil {
			return nil, fmt.Errorf("upload source package %s: %w", packageName, err)
		}
		return nil, fmt.Errorf("upload source package %s: empty file", packageName)
	}
	pkg := p.Paragraph()
	dir := poolPath(packageName)
	transformSourceControl(&pkg, dir)
	files, err := deb.ParseIndexSignatures(pkg.Get("Files"), md5.Size)
	if err != nil {
		return nil, fmt.Errorf("upload source package %s: files: %w", packageName, err)
	}

	_, err = upload(ctx, bucket, dir+"/"+filepath.Base(dscPath), bytes.NewReader(dsc), uploadOptions{
		contentType:  "text/plain; charset=utf-8",
		cacheControl: immutable,
	})
	if err != nil {
		return nil, fmt.Errorf("upload source package %s: %s: %w", packageName, filepath.Base(dscPath), err)
	}
	for _, sig := range files {
		fname := sig.Filename
		contentType := mime.TypeByExtension(slashpath.Ext(fname))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		content, err := os.Open(filepath.Join(filepath.Dir(dscPath), fname))
		if err != nil {
			return nil, fmt.Errorf("upload source package %s: %s: %w", packageName, fname, err)
		}
		_, uploadErr := upload(ctx, bucket, dir+"/"+fname, content, uploadOptions{
			contentType:  contentType,
			cacheControl: immutable,
		})
		content.Close()
		if uploadErr != nil {
			return nil, fmt.Errorf("upload source package %s: %s: %w", packageName, fname, err)
		}
	}
	return pkg, nil
}

// maybeClearSigned returns the plaintext of a file that may or may not be
// wrapped in GPG clear-signed armor.
func maybeClearSigned(data []byte) []byte {
	block, _ := clearsign.Decode(data)
	if block == nil {
		return data
	}
	return block.Plaintext
}

// promotePackageField ensures the Package field is the first in the paragraph.
// It modifies the paragraph in-place.
//
// This is necessary for Packages and Sources paragraphs to be spec-compliant.
func promotePackageField(para deb.Paragraph) {
	for i, f := range para {
		if f.Name == "Package" {
			copy(para[1:], para[:i])
			para[0] = f
			return
		}
	}
}

// transformSourceControl changes a Debian source control paragraph to a Sources
// index paragraph.
func transformSourceControl(para *deb.Paragraph, dir string) {
	for i := range *para {
		if (*para)[i].Name == "Source" {
			(*para)[i].Name = "Package"
		}
	}
	promotePackageField(*para)
	para.Set("Directory", dir)
}

func poolPath(name string) string {
	return "pool/" + name
}

// immutable is the Cache-Control header that indicates that the content is immutable.
const immutable = "immutable"

type uploadOptions struct {
	contentType  string
	cacheControl string
}

func upload(ctx context.Context, bucket *blob.Bucket, key string, content io.ReadSeeker, opts uploadOptions) (indexHashes, error) {
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return indexHashes{}, fmt.Errorf("upload %s: %w", key, err)
	}
	md5Hash := md5.New()
	sha1Hash := sha1.New()
	sha256Hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(md5Hash, sha1Hash, sha256Hash), content)
	if err != nil {
		return indexHashes{}, fmt.Errorf("upload %s: %w", key, err)
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return indexHashes{}, fmt.Errorf("upload %s: %w", key, err)
	}

	var h indexHashes
	h.size = size
	md5Hash.Sum(h.md5[:0])
	sha1Hash.Sum(h.sha1[:0])
	sha256Hash.Sum(h.sha256[:0])
	if opts.cacheControl == immutable {
		attr, err := bucket.Attributes(ctx, key)
		if err == nil {
			// Immutable objects don't have to be uploaded if they already exist,
			// but they must match the existing object.
			if attr.Size != h.size || !bytes.Equal(h.md5[:], attr.MD5) {
				return indexHashes{}, fmt.Errorf("upload %s: immutable object differs", key)
			}
			return h, nil
		} else if gcerrors.Code(err) != gcerrors.NotFound {
			return indexHashes{}, fmt.Errorf("upload %s: %w", key, err)
		}
	}
	if opts.cacheControl == "" {
		// Default to 5 minute cache.
		opts.cacheControl = "max-age=300"
	}
	w, err := bucket.NewWriter(ctx, key, &blob.WriterOptions{
		ContentType:  opts.contentType,
		ContentMD5:   h.md5[:],
		CacheControl: opts.cacheControl,
	})
	if err != nil {
		return indexHashes{}, fmt.Errorf("upload %s: %w", key, err)
	}
	_, writeErr := io.Copy(w, content)
	closeErr := w.Close()
	if writeErr != nil {
		return indexHashes{}, fmt.Errorf("upload %s: %w", key, writeErr)
	}
	if closeErr != nil {
		return indexHashes{}, fmt.Errorf("upload %s: %w", key, closeErr)
	}
	return h, nil
}
