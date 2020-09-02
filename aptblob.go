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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	newRelease.Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 Z"))
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

func cmdUpload(ctx context.Context, bucket *blob.Bucket, comp component, keyID string, paths []string) error {
	release, err := downloadReleaseIndex(ctx, bucket, comp.dist)
	if err != nil {
		return err
	}
	addToTokenSet(&release, "Components", comp.name)

	binaryAdditions := make(map[string][]deb.Paragraph)
	var sourceAdditions []deb.Paragraph
	for _, path := range paths {
		switch filepath.Ext(path) {
		case ".deb":
			pkg, err := uploadBinaryPackage(ctx, bucket, path)
			if err != nil {
				return err
			}
			arch := pkg.Get("Architecture")
			if arch == "all" {
				for _, arch := range strings.Fields(release.Get("Architectures")) {
					binaryAdditions[arch] = append(binaryAdditions[arch], pkg)
				}
				continue
			}
			addToTokenSet(&release, "Architectures", arch)
			binaryAdditions[arch] = append(binaryAdditions[arch], pkg)
		case ".dsc":
			pkg, err := uploadSourcePackage(ctx, bucket, path)
			if err != nil {
				return err
			}
			sourceAdditions = append(sourceAdditions, pkg)
		default:
			return fmt.Errorf("%s: unrecognized extension", path)
		}
	}

	for arch, packages := range binaryAdditions {
		err := appendToIndex(ctx,
			bucket,
			comp.dist,
			&release,
			comp.binaryIndexPath(arch),
			deb.ControlFields,
			packages,
		)
		if err != nil {
			return err
		}
	}
	err = appendToIndex(ctx,
		bucket,
		comp.dist,
		&release,
		comp.sourceIndexPath(),
		deb.SourceControlFields,
		sourceAdditions,
	)
	if err != nil {
		return err
	}

	release.Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 Z"))
	if err := uploadReleaseIndex(ctx, bucket, comp.dist, release, keyID); err != nil {
		return err
	}

	return nil
}

func appendToIndex(ctx context.Context, bucket *blob.Bucket, dist distribution, release *deb.Paragraph, key string, fields map[string]deb.FieldType, newParagraphs []deb.Paragraph) error {
	if len(newParagraphs) == 0 {
		return nil
	}

	// List existing packages.
	packages, err := downloadIndex(ctx, bucket, key, fields)
	if err != nil {
		return err
	}

	// Append packages to index.
	packages, err = dedupePackages(append(packages, newParagraphs...))
	if err != nil {
		return err
	}
	indexHashes, gzipIndexHashes, err := uploadIndex(ctx, bucket, key, packages)
	if err != nil {
		return err
	}

	// Update release signatures.
	distPath := strings.TrimPrefix(key, dist.dir()+"/")
	gzipDistPath := distPath + gzipExtension
	err = updateSignature(release, "MD5Sum",
		deb.IndexSignature{
			Filename: distPath,
			Checksum: indexHashes.md5[:],
			Size:     indexHashes.size,
		},
		deb.IndexSignature{
			Filename: gzipDistPath,
			Checksum: gzipIndexHashes.md5[:],
			Size:     gzipIndexHashes.size,
		},
	)
	if err != nil {
		return fmt.Errorf("%s: %w", dist.indexPath(), err)
	}
	err = updateSignature(release, "SHA1",
		deb.IndexSignature{
			Filename: distPath,
			Checksum: indexHashes.sha1[:],
			Size:     indexHashes.size,
		},
		deb.IndexSignature{
			Filename: gzipDistPath,
			Checksum: gzipIndexHashes.sha1[:],
			Size:     gzipIndexHashes.size,
		},
	)
	if err != nil {
		return fmt.Errorf("%s: %w", dist.indexPath(), err)
	}
	err = updateSignature(release, "SHA256",
		deb.IndexSignature{
			Filename: distPath,
			Checksum: indexHashes.sha256[:],
			Size:     indexHashes.size,
		},
		deb.IndexSignature{
			Filename: gzipDistPath,
			Checksum: gzipIndexHashes.sha256[:],
			Size:     gzipIndexHashes.size,
		},
	)
	if err != nil {
		return fmt.Errorf("%s: %w", dist.indexPath(), err)
	}
	return nil
}

func downloadIndex(ctx context.Context, bucket *blob.Bucket, key string, fields map[string]deb.FieldType) ([]deb.Paragraph, error) {
	r, err := bucket.NewReader(ctx, key, nil)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	defer r.Close()
	p := deb.NewParser(r)
	p.Fields = fields
	var paragraphs []deb.Paragraph
	for p.Next() {
		paragraphs = append(paragraphs, append(deb.Paragraph(nil), p.Paragraph()...))
	}
	if err := p.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return paragraphs, nil
}

func dedupePackages(packages []deb.Paragraph) ([]deb.Paragraph, error) {
	type packageVersion struct {
		name    string
		version string
	}
	index := make(map[packageVersion]int)
	n := 0
	for _, pkg := range packages {
		v := packageVersion{
			name:    pkg.Get("Package"),
			version: pkg.Get("Version"),
		}
		if v.name == "" || v.version == "" {
			return nil, errors.New("package found without Package or Version")
		}
		i, seen := index[v]
		if !seen {
			i = n
			n++
		}
		packages[i] = pkg
		index[v] = i
	}
	return packages[:n], nil
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
	uploadCmd := &cobra.Command{
		Use:                   "upload [options] BUCKET DIST PACKAGE [...]",
		Short:                 "Upload one or more packages",
		Args:                  cobra.MinimumNArgs(3),
		DisableFlagsInUseLine: true,
		SilenceErrors:         true,
		SilenceUsage:          true,
	}
	uploadComponentName := uploadCmd.Flags().StringP("component", "c", "main", "component name")
	uploadCmd.RunE = func(cmd *cobra.Command, args []string) error {
		bucket, err := blob.OpenBucket(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		comp := component{
			dist: distribution(args[1]),
			name: *uploadComponentName,
		}
		return cmdUpload(cmd.Context(), bucket, comp, *keyID, args[2:])
	}
	rootCmd.AddCommand(uploadCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "aptblob:", err)
		os.Exit(1)
	}
}

func addToTokenSet(para *deb.Paragraph, key string, s string) {
	var f *deb.Field
	for i := range *para {
		if (*para)[i].Name == key {
			f = &(*para)[i]
		}
	}
	if f == nil {
		*para = append(*para, deb.Field{Name: key, Value: s})
		return
	}
	elems := strings.Fields(f.Value)
	for _, e := range elems {
		if e == s {
			return
		}
	}
	elems = append(elems, s)
	f.Value = strings.Join(elems, " ")
	return
}
