package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type BundleIndex struct {
	APIVersion string                `json:"apiVersion"`
	Entries    []BundleIndexEntry    `json:"entries"`
	Signature  *BundleIndexSignature `json:"signature,omitempty"`
}

type BundleIndexEntry struct {
	GameDefinitionID  string `json:"gameDefinitionId"`
	DefinitionVersion string `json:"definitionVersion"`
	GameVersion       string `json:"gameVersion"`
	Digest            string `json:"digest"`
	Bundle            string `json:"bundle"`
	KeyID             string `json:"keyId"`
}

type BundleIndexSignature = BundleSignature

func indexSigningPayload(index BundleIndex) ([]byte, string, error) {
	index.Signature = nil
	canonical, err := json.Marshal(index)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(canonical)
	return canonical, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func signBundleIndex(index BundleIndex, private ed25519.PrivateKey) (BundleIndex, error) {
	payload, digest, err := indexSigningPayload(index)
	if err != nil {
		return BundleIndex{}, err
	}
	public := private.Public().(ed25519.PublicKey)
	index.Signature = &BundleIndexSignature{
		Algorithm: "ed25519", KeyID: publicKeyID(public), PayloadDigest: digest,
		Value: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, payload)),
	}
	return index, nil
}

func indexBuildCommand(args []string) {
	flags := flag.NewFlagSet("bundle index-build", flag.ContinueOnError)
	output := flags.String("out", "", "catalog index output")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 || *output == "" {
		bundleUsage()
		os.Exit(2)
	}
	index := BundleIndex{APIVersion: "gugumanager.io/catalog/v1", Entries: make([]BundleIndexEntry, 0, flags.NArg())}
	for _, filename := range flags.Args() {
		bundle, err := readBundle(filename)
		if err != nil || bundle.Signature == nil {
			fmt.Fprintf(os.Stderr, "bundle index-build failed: %s is not a signed Bundle: %v\n", filename, err)
			os.Exit(1)
		}
		index.Entries = append(index.Entries, BundleIndexEntry{
			GameDefinitionID: bundle.GameDefinitionID, DefinitionVersion: bundle.DefinitionVersion,
			GameVersion: bundle.GameVersion, Digest: bundle.Digest, Bundle: filepath.Base(filename), KeyID: bundle.Signature.KeyID,
		})
	}
	sort.Slice(index.Entries, func(i, j int) bool {
		if index.Entries[i].GameDefinitionID == index.Entries[j].GameDefinitionID {
			return index.Entries[i].DefinitionVersion < index.Entries[j].DefinitionVersion
		}
		return index.Entries[i].GameDefinitionID < index.Entries[j].GameDefinitionID
	})
	encoded, _ := json.MarshalIndent(index, "", "  ")
	if err := atomicWriteBytes(*output, append(encoded, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "bundle index-build failed: %v\n", err)
		os.Exit(1)
	}
}

func indexSignCommand(args []string) {
	flags := flag.NewFlagSet("bundle index-sign", flag.ContinueOnError)
	keyPath := flags.String("key", "", "Ed25519 PKCS#8 private key")
	output := flags.String("out", "", "signed catalog index output")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || *keyPath == "" || *output == "" {
		bundleUsage()
		os.Exit(2)
	}
	content, err := os.ReadFile(flags.Arg(0))
	var index BundleIndex
	if err == nil {
		err = json.Unmarshal(content, &index)
	}
	if err == nil && (index.APIVersion != "gugumanager.io/catalog/v1" || index.Signature != nil) {
		err = errors.New("catalog index is invalid or already signed")
	}
	var private ed25519.PrivateKey
	if err == nil {
		private, err = loadEd25519PrivateKey(*keyPath)
	}
	if err == nil {
		index, err = signBundleIndex(index, private)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bundle index-sign failed: %v\n", err)
		os.Exit(1)
	}
	encoded, _ := json.MarshalIndent(index, "", "  ")
	if err := atomicWriteBytes(*output, append(encoded, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "bundle index-sign failed: %v\n", err)
		os.Exit(1)
	}
}
