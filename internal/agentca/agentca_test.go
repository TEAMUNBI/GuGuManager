package agentca

import (
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
