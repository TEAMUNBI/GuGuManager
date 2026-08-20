package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type BundleSignature struct {
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"keyId"`
	PayloadDigest string `json:"payloadDigest"`
	Value         string `json:"value"`
}

func publicKeyID(public ed25519.PublicKey) string {
	digest := sha256.Sum256(public)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func bundleSigningPayload(bundle Bundle) ([]byte, string, error) {
	bundle.Signature = nil
	canonical, err := json.Marshal(bundle)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(canonical)
	return canonical, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func signBundle(bundle Bundle, private ed25519.PrivateKey) (Bundle, error) {
	if len(private) != ed25519.PrivateKeySize {
		return Bundle{}, errors.New("invalid Ed25519 private key")
	}
	payload, digest, err := bundleSigningPayload(bundle)
	if err != nil {
		return Bundle{}, err
	}
	public := private.Public().(ed25519.PublicKey)
	bundle.Signature = &BundleSignature{
		Algorithm: "ed25519", KeyID: publicKeyID(public), PayloadDigest: digest,
		Value: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, payload)),
	}
	return bundle, nil
}

func verifyBundleSignature(bundle Bundle, public ed25519.PublicKey) error {
	signature := bundle.Signature
	if signature == nil || signature.Algorithm != "ed25519" || signature.KeyID != publicKeyID(public) {
		return errors.New("bundle signature is missing or does not match the trust root")
	}
	payload, digest, err := bundleSigningPayload(bundle)
	if err != nil {
		return err
	}
	if signature.PayloadDigest != digest {
		return errors.New("bundle signed payload digest mismatch")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(signature.Value)
	if err != nil || len(decoded) != ed25519.SignatureSize || !ed25519.Verify(public, payload, decoded) {
		return errors.New("bundle Ed25519 signature verification failed")
	}
	return nil
}

func loadEd25519PrivateKey(filename string) (ed25519.PrivateKey, error) {
	var content []byte
	var err error
	if filename == "-" {
		content, err = io.ReadAll(os.Stdin)
	} else {
		content, err = os.ReadFile(filename)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("private key must be an unencrypted PKCS#8 PEM PRIVATE KEY")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}
	return private, nil
}

func loadEd25519PublicKey(filename string) (ed25519.PublicKey, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("trust root must be a PKIX PEM PUBLIC KEY")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("trust root is not Ed25519")
	}
	return public, nil
}

func marshalEd25519PublicKeyPEM(public ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

func readBundle(filename string) (Bundle, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, err
	}
	if !isBundleDigest(bundle.Digest) {
		return Bundle{}, fmt.Errorf("digest %q is not a sha256:<64 lowercase hex> value", bundle.Digest)
	}
	return bundle, nil
}

func writeBundle(filename string, bundle Bundle) error {
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return atomicWriteBytes(filename, encoded)
}

func atomicWriteBytes(filename string, content []byte) error {
	directory := filepath.Dir(filename)
	temporary, err := os.CreateTemp(directory, ".bundle-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filename); err == nil {
		return nil
	} else if _, statErr := os.Stat(filename); statErr != nil {
		return err
	}
	// Windows does not replace an existing destination with os.Rename. The
	// temporary file is already fsynced, so use the narrow fallback only for an
	// explicitly requested existing output path.
	if err := os.Remove(filename); err != nil {
		return err
	}
	return os.Rename(temporaryName, filename)
}
