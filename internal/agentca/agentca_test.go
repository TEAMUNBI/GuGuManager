package agentca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCABasicLifecycle(t *testing.T) {
	dir := t.TempDir()
	ca, err := NewCA(dir)
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}
	certPEM, err := ca.IssueAgentCertificate("node-1", 24*time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := ca.VerifyPeerCertificate(certPEM, "node-1"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := ca.VerifyPeerCertificate(certPEM, "node-2"); err == nil {
		t.Fatal("expected subject mismatch to fail")
	}
	// 证书应可用作 client auth 且由 CA 签发
	if err := ca.VerifyCertificateChain(certPEM); err != nil {
		t.Fatalf("chain verify: %v", err)
	}
}

func TestIssueAgentCertificateFromCSR(t *testing.T) {
	dir := t.TempDir()
	ca, err := NewCA(dir)
	if err != nil {
		t.Fatalf("new ca: %v", err)
	}

	// 生成 Agent 侧密钥与 CSR（CN=node-1），并要求 CA 基于 CSR 公钥签发证书。
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkixName("node-1"),
	}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	certPEM, err := ca.IssueAgentCertificateFromCSR(csrPEM, "node-1", 24*time.Hour)
	if err != nil {
		t.Fatalf("issue from csr: %v", err)
	}
	if err := ca.VerifyPeerCertificate(certPEM, "node-1"); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// 关键断言：签发的证书必须对应 CSR 的公钥，Agent 才能用原私钥完成 mTLS。
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	if !cert.PublicKey.(*rsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatal("issued certificate public key does not match csr public key")
	}
	// 证书 CN 固定为 nodeID（而非 CSR 的 Subject CN）——节点身份以注册返回的
	// UUID 为准，CSR CN 只是标签。
	if cert.Subject.CommonName != "node-1" {
		t.Fatalf("issued certificate CN = %q, want node-1", cert.Subject.CommonName)
	}

	// CSR 的 Subject CN 与 nodeID 不一致时仍应签发（证书 CN 以 nodeID 为准）。
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	otherCSRDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkixName("node-other"),
	}, other)
	if err != nil {
		t.Fatalf("create other csr: %v", err)
	}
	otherCSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: otherCSRDER})
	certPEM2, err := ca.IssueAgentCertificateFromCSR(otherCSR, "node-1", 24*time.Hour)
	if err != nil {
		t.Fatalf("issue from csr with different subject: %v", err)
	}
	block2, _ := pem.Decode(certPEM2)
	cert2, err := x509.ParseCertificate(block2.Bytes)
	if err != nil {
		t.Fatalf("parse second issued cert: %v", err)
	}
	if cert2.Subject.CommonName != "node-1" {
		t.Fatalf("second issued certificate CN = %q, want node-1", cert2.Subject.CommonName)
	}
}

func TestNewCAAcceptsECDSARoot(t *testing.T) {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa ca key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "GuGuManager Root CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	root, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create ecdsa root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}), 0o644); err != nil {
		t.Fatalf("write ecdsa cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal ecdsa key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write ecdsa key: %v", err)
	}

	ca, err := NewCA(dir)
	if err != nil {
		t.Fatalf("load ecdsa ca: %v", err)
	}
	certPEM, err := ca.IssueAgentCertificate("ecdsa-node", time.Hour)
	if err != nil {
		t.Fatalf("issue from ecdsa ca: %v", err)
	}
	if err := ca.VerifyPeerCertificate(certPEM, "ecdsa-node"); err != nil {
		t.Fatalf("verify ecdsa leaf: %v", err)
	}
}

func pkixName(cn string) pkix.Name {
	return pkix.Name{CommonName: cn}
}
