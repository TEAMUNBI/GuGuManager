package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestBundleEd25519SignatureRejectsTampering(t *testing.T) {
	bundle, err := buildBundle(examplePath())
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signBundle(*bundle, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleSignature(signed, public); err != nil {
		t.Fatalf("verify signed bundle: %v", err)
	}
	signed.GameVersion += "-tampered"
	if err := verifyBundleSignature(signed, public); err == nil {
		t.Fatal("signature accepted a tampered Bundle revision")
	}
}

func TestBundleEd25519SignatureIsDeterministic(t *testing.T) {
	bundle, err := buildBundle(examplePath())
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := signBundle(*bundle, private)
	second, _ := signBundle(*bundle, private)
	if first.Signature.Value != second.Signature.Value || first.Signature.PayloadDigest != second.Signature.PayloadDigest {
		t.Fatal("identical bundle signing was not reproducible")
	}
}
