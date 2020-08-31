package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"

	"gocloud.dev/blob"
	"zombiezen.com/go/aptblob/internal/deb"
)

func poolPath(name string) string {
	return "pool/" + name
}

func distDirPath(dist string) string {
	return "dists/" + dist
}

func releaseIndexPath(dist string) string {
	return distDirPath(dist) + "/Release"
}

func signedReleaseIndexPath(dist string) string {
	return distDirPath(dist) + "/InRelease"
}

func releaseSignatureIndexPath(dist string) string {
	return distDirPath(dist) + "/Release.gpg"
}

func binaryPackagesIndexPath(dist, component, arch string) string {
	return distDirPath(dist) + "/" + component + "/binary-" + arch + "/Packages"
}

func binaryPackagesGzipIndexPath(dist, component, arch string) string {
	return distDirPath(dist) + "/" + component + "/binary-" + arch + "/Packages.gz"
}

func uploadReleaseIndex(ctx context.Context, bucket *blob.Bucket, dist string, release deb.Paragraph, keyID string) error {
	data := new(bytes.Buffer)
	deb.Save(data, []deb.Paragraph{release})
	err := bucket.WriteAll(ctx, releaseIndexPath(dist), data.Bytes(), &blob.WriterOptions{
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
	err = bucket.WriteAll(ctx, signedReleaseIndexPath(dist), clearSignOutput.Bytes(), &blob.WriterOptions{
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
	err = bucket.WriteAll(ctx, releaseSignatureIndexPath(dist), detachSignOutput.Bytes(), &blob.WriterOptions{
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

func uploadPackageIndex(ctx context.Context, bucket *blob.Bucket, dist, component, arch string, packages []deb.Paragraph) (uncompressed, compressed indexHashes, err error) {
	buf := new(bytes.Buffer)
	if err := deb.Save(buf, packages); err != nil {
		return indexHashes{}, indexHashes{}, err
	}
	key := binaryPackagesIndexPath(dist, component, arch)
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
	compressed, err = upload(ctx, bucket, binaryPackagesGzipIndexPath(dist, component, arch), "application/gzip", "", bytes.NewReader(gzipBuf.Bytes()))
	if err != nil {
		return indexHashes{}, indexHashes{}, err
	}
	return
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
