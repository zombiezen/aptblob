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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"gocloud.dev/blob"
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

func (comp component) binaryIndexGzipPath(arch string) string {
	return comp.binaryIndexPath(arch) + ".gz"
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

func uploadPackageIndex(ctx context.Context, bucket *blob.Bucket, comp component, arch string, packages []deb.Paragraph) (uncompressed, compressed indexHashes, err error) {
	buf := new(bytes.Buffer)
	if err := deb.Save(buf, packages); err != nil {
		return indexHashes{}, indexHashes{}, err
	}
	key := comp.binaryIndexPath(arch)
	uncompressed, err = upload(ctx, bucket, key, "text/plain; charset=utf-8", "", bytes.NewReader(buf.Bytes()))
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
	compressed, err = upload(ctx, bucket, comp.binaryIndexGzipPath(arch), "application/gzip", "", bytes.NewReader(gzipBuf.Bytes()))
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
			return nil, fmt.Errorf("upload binary package %s: %w", debName, err)
		}
	}
	pkg := p.Paragraph()
	promotePackageField(pkg)
	arch := pkg.Get("Architecture")
	if arch == "" {
		return nil, fmt.Errorf("upload binary package %s: missing Architecture field", debName)
	}
	packageHashes, err := upload(ctx, bucket, poolPath(debName), "application/vnd.debian.binary-package", "immutable", debFile)
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

func poolPath(name string) string {
	return "pool/" + name
}

func upload(ctx context.Context, bucket *blob.Bucket, key string, contentType, cacheControl string, content io.ReadSeeker) (indexHashes, error) {
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
	w, err := bucket.NewWriter(ctx, key, &blob.WriterOptions{
		ContentType:  contentType,
		ContentMD5:   h.md5[:],
		CacheControl: cacheControl,
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
