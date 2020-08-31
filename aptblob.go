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
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/s3blob"
	"gocloud.dev/gcerrors"
	"zombiezen.com/go/aptblob/internal/deb"
)

func cmdInit(ctx context.Context, bucket *blob.Bucket, dist distribution, keyID string) error {
	fmt.Fprintln(os.Stderr, "aptblob: reading Release from stdin...")
	newRelease, err := deb.ParseReleaseIndex(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	oldRelease, err := downloadReleaseIndex(ctx, bucket, dist)
	if err != nil {
		return fmt.Errorf("read old release: %w", err)
	}
	keys := []string{"MD5Sum", "SHA1", "SHA256"}
	for _, k := range keys {
		if v := oldRelease.Get(k); v != "" {
			newRelease.Set(k, v)
		}
	}
	err = uploadReleaseIndex(ctx, bucket, dist, newRelease, keyID)
	if err != nil {
		return err
	}
	return nil
}

func downloadReleaseIndex(ctx context.Context, bucket *blob.Bucket, dist distribution) (deb.Paragraph, error) {
	key := dist.indexPath()
	blob, err := bucket.NewReader(ctx, key, nil)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, nil
		}
		return nil, err
	}
	index, err := deb.ParseReleaseIndex(blob)
	blob.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return index, nil
}

const componentName = "main"

func cmdUpload(ctx context.Context, bucket *blob.Bucket, dist distribution, keyID string, debPath string) error {
	// Extract package metadata.
	debFile, err := os.Open(debPath)
	if err != nil {
		return err
	}
	defer debFile.Close()
	control, err := deb.ExtractControl(debFile)
	if err != nil {
		return fmt.Errorf("%s: %w", debPath, err)
	}
	p := deb.NewParser(bytes.NewReader(control))
	p.Fields = deb.ControlFields
	if !p.Single() {
		if err := p.Err(); err != nil {
			return fmt.Errorf("%s: %w", debPath, err)
		}
	}
	newPackage := p.Paragraph()
	promotePackageField(newPackage)
	arch := newPackage.Get("Architecture")
	if arch == "" {
		return fmt.Errorf("%s: no Architecture", debPath)
	}
	poolPath := poolPath(filepath.Base(debPath))
	packageHashes, err := upload(ctx, bucket, poolPath, "application/vnd.debian.binary-package", "immutable", debFile)
	if err != nil {
		return err
	}
	newPackage.Set("Filename", poolPath)
	newPackage.Set("Size", strconv.FormatInt(packageHashes.size, 10))
	newPackage.Set("MD5sum", hex.EncodeToString(packageHashes.md5[:]))
	newPackage.Set("SHA1", hex.EncodeToString(packageHashes.sha1[:]))
	newPackage.Set("SHA256", hex.EncodeToString(packageHashes.sha256[:]))

	// List existing packages.
	var packages []deb.Paragraph
	comp := component{dist: dist, name: componentName}
	if packagesReader, err := bucket.NewReader(ctx, comp.binaryIndexPath(arch), nil); err == nil {
		p := deb.NewParser(packagesReader)
		p.Fields = deb.ControlFields
		for p.Next() {
			packages = append(packages, p.Paragraph())
		}
		packagesReader.Close()
		if err := p.Err(); err != nil {
			return fmt.Errorf("%s: %w", comp.binaryIndexPath(arch), err)
		}
	} else if gcerrors.Code(err) != gcerrors.NotFound {
		return err
	}

	// Append package to index.
	packages = append(packages, newPackage)
	packageIndexHashes, packageIndexGzipHashes, err :=
		uploadPackageIndex(ctx, bucket, comp, arch, packages)
	if err != nil {
		return err
	}

	// Update release index.
	release, err := downloadReleaseIndex(ctx, bucket, dist)
	if err != nil {
		return err
	}
	packagesDistPath := strings.TrimPrefix(comp.binaryIndexPath(arch), dist.dir()+"/")
	packagesGzipDistPath := strings.TrimPrefix(comp.binaryIndexGzipPath(arch), dist.dir()+"/")
	err = updateSignature(&release, "MD5Sum",
		deb.IndexSignature{
			Filename: packagesDistPath,
			Checksum: packageIndexHashes.md5[:],
			Size:     packageIndexHashes.size,
		},
		deb.IndexSignature{
			Filename: packagesGzipDistPath,
			Checksum: packageIndexGzipHashes.md5[:],
			Size:     packageIndexGzipHashes.size,
		},
	)
	if err != nil {
		return fmt.Errorf("%s: %w", dist.indexPath(), err)
	}
	err = updateSignature(&release, "SHA1",
		deb.IndexSignature{
			Filename: packagesDistPath,
			Checksum: packageIndexHashes.sha1[:],
			Size:     packageIndexHashes.size,
		},
		deb.IndexSignature{
			Filename: packagesGzipDistPath,
			Checksum: packageIndexGzipHashes.sha1[:],
			Size:     packageIndexGzipHashes.size,
		},
	)
	if err != nil {
		return fmt.Errorf("%s: %w", dist.indexPath(), err)
	}
	err = updateSignature(&release, "SHA256",
		deb.IndexSignature{
			Filename: packagesDistPath,
			Checksum: packageIndexHashes.sha256[:],
			Size:     packageIndexHashes.size,
		},
		deb.IndexSignature{
			Filename: packagesGzipDistPath,
			Checksum: packageIndexGzipHashes.sha256[:],
			Size:     packageIndexGzipHashes.size,
		},
	)
	if err != nil {
		return fmt.Errorf("%s: %w", dist.indexPath(), err)
	}
	if err := uploadReleaseIndex(ctx, bucket, dist, release, keyID); err != nil {
		return err
	}

	return nil
}

func updateSignature(para *deb.Paragraph, key string, newSigs ...deb.IndexSignature) error {
	if len(newSigs) == 0 {
		return nil
	}
	sigs, err := deb.ParseIndexSignatures(para.Get(key), len(newSigs[0].Checksum))
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	newMap := make(map[string]deb.IndexSignature, len(newSigs))
	for _, sig := range newSigs {
		newMap[sig.Filename] = sig
	}
	for i := range sigs {
		newSig, ok := newMap[sigs[i].Filename]
		if !ok {
			continue
		}
		sigs[i].Checksum = newSig.Checksum
		sigs[i].Size = newSig.Size
		delete(newMap, sigs[i].Filename)
	}
	for _, sig := range newSigs {
		if _, ok := newMap[sig.Filename]; !ok {
			// Already added.
			continue
		}
		sigs = append(sigs, sig)
		delete(newMap, sig.Filename)
	}
	sb := new(strings.Builder)
	for _, sig := range sigs {
		sb.WriteString("\n ")
		sb.WriteString(sig.String())
	}
	para.Set(key, sb.String())
	return nil
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

func main() {
	rootCmd := &cobra.Command{
		Use:           "aptblob",
		Short:         "Manager for blob-storage-based APT repositories",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("must have at least one argument for bucket")
			}
			var err error
			return err
		},
	}
	keyID := rootCmd.PersistentFlags().StringP("keyid", "k", "", "GPG key to sign with")
	rootCmd.AddCommand(&cobra.Command{
		Use:                   "init [options] BUCKET DIST",
		Short:                 "Set up a distribution",
		Args:                  cobra.ExactArgs(2),
		DisableFlagsInUseLine: true,
		SilenceErrors:         true,
		SilenceUsage:          true,
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, err := blob.OpenBucket(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return cmdInit(cmd.Context(), bucket, distribution(args[1]), *keyID)
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:                   "upload [options] BUCKET DIST DEB",
		Short:                 "Upload a deb package",
		Args:                  cobra.ExactArgs(3),
		DisableFlagsInUseLine: true,
		SilenceErrors:         true,
		SilenceUsage:          true,
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, err := blob.OpenBucket(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return cmdUpload(cmd.Context(), bucket, distribution(args[1]), *keyID, args[2])
		},
	})
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "aptblob:", err)
		os.Exit(1)
	}
}
