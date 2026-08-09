package agentca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile = "ca.crt"
	keyFile  = "ca.key"
	caTTL    = 10 * 365 * 24 * time.Hour
)

// CA 管理 Agent 的 mTLS 证书签发与校验。
// 根证书与私钥在首次创建时生成并持久化到 certDir：
//   - ca.crt      PEM 根证书
//   - ca.key      PEM 根私钥（权限受限）
type CA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
	dir  string
}

// NewCA 加载或首次生成根 CA。证书与私钥持久化在 dir 下的 ca.crt / ca.key。
func NewCA(dir string) (*CA, error) {
	certPath := filepath.Join(dir, certFile)
	keyPath := filepath.Join(dir, keyFile)

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		ca, err := parseCA(certPEM, keyPEM)
		if err != nil {
			return nil, err
		}
		ca.dir = dir
		return ca, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create ca dir: %w", err)
	}
	return generateCA(dir, certPath, keyPath)
}

// RootCAPEM 返回根证书 PEM，供 Agent 建立信任。
func (c *CA) RootCAPEM() ([]byte, error) {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw}), nil
}

// IssueAgentCertificate 生成 RSA 2048 叶密钥并签发 CN=nodeID 的 client auth 叶证书，
// 返回其 PEM。叶私钥不持久化、不返回（Agent 侧如需建立 mTLS 请使用
// IssueAgentCertificateWithKey 或自行提供 CSR）。
func (c *CA) IssueAgentCertificate(nodeID string, ttl time.Duration) ([]byte, error) {
	certPEM, _, err := c.IssueAgentCertificateWithKey(nodeID, ttl)
	return certPEM, err
}

// IssueAgentCertificateWithKey 生成 RSA 2048 叶密钥并用根私钥签发 CN=nodeID 的
// client auth 叶证书，同时返回证书与私钥 PEM，供 Agent 建立 mTLS 连接使用。
func (c *CA) IssueAgentCertificateWithKey(nodeID string, ttl time.Duration) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf key: %w", err)
	}
	certPEM, err := c.signLeaf(pkix.Name{CommonName: nodeID}, x509.ExtKeyUsageClientAuth, ttl, &key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// IssueAgentCertificateFromCSR 基于 Agent 提交的 PKCS#10 CSR 签发 client auth
// 叶证书（CN 固定为 nodeID）。CSR 只承担公钥传输：其 Subject CN 只是节点标签，
// 注册令牌已完成身份认证，因此不校验 CSR CN。签发结果使用 CSR 中的公钥，
// 保证 Agent 持有的私钥与证书匹配，是真实 mTLS 注册与轮换的必经路径。
func (c *CA) IssueAgentCertificateFromCSR(csrPEM []byte, nodeID string, ttl time.Duration) ([]byte, error) {
	csr, err := parseCSR(csrPEM)
	if err != nil {
		return nil, err
	}
	return c.signLeaf(pkix.Name{CommonName: nodeID}, x509.ExtKeyUsageClientAuth, ttl, csr.PublicKey)
}

// parseCSR 解析 PEM CSR，要求恰好包含一个 CERTIFICATE REQUEST 块。
func parseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("no CERTIFICATE REQUEST block in csr")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate request: %w", err)
	}
	return csr, nil
}

// IssueServerCertificate 生成 RSA 2048 密钥并签发 CN="control-plane" 的 server
// auth 证书，返回证书与私钥 PEM，供 Control Plane 的 gRPC TLS 监听使用。
func (c *CA) IssueServerCertificate(ttl time.Duration) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate server key: %w", err)
	}
	certPEM, err := c.signLeaf(pkix.Name{CommonName: "control-plane"}, x509.ExtKeyUsageServerAuth, ttl, &key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// IssueServerCertificateWithSAN 签发 server auth 证书，额外携带指定的 IP SAN，
// 使 Agent 可用 IP 地址（而非仅 DNS 名）校验证书链。
func (c *CA) IssueServerCertificateWithSAN(ttl time.Duration, ips []net.IP) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate server key: %w", err)
	}
	certPEM, err := c.signLeafWithIPs(pkix.Name{CommonName: "control-plane"}, x509.ExtKeyUsageServerAuth, ttl, &key.PublicKey, ips)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// signLeaf 用根私钥签发一张由本 CA 签发的叶证书。
// 证书同时携带 CN 与对应的 DNS SAN，满足现代 x509 校验（Go 1.26 拒绝仅有 CN 的证书）。
func (c *CA) signLeaf(subject pkix.Name, extKeyUsage x509.ExtKeyUsage, ttl time.Duration, pub any) ([]byte, error) {
	return c.signLeafWithIPs(subject, extKeyUsage, ttl, pub, nil)
}

func (c *CA) signLeafWithIPs(subject pkix.Name, extKeyUsage x509.ExtKeyUsage, ttl time.Duration, pub any, ips []net.IP) ([]byte, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		DNSNames:     []string{subject.CommonName},
		IPAddresses:  ips,
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{extKeyUsage},
	}, c.cert, pub, c.key)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// VerifyPeerCertificate 校验证书链与 client auth 用途，并要求 CN 与 expectedNodeID 一致。
func (c *CA) VerifyPeerCertificate(certPEM []byte, expectedNodeID string) error {
	cert, err := parseLeaf(certPEM)
	if err != nil {
		return err
	}
	if err := c.verifyChain(cert); err != nil {
		return err
	}
	if cert.Subject.CommonName != expectedNodeID {
		return fmt.Errorf("subject mismatch: expected CN=%q, got CN=%q", expectedNodeID, cert.Subject.CommonName)
	}
	return nil
}

// VerifyCertificateChain 仅校验证书链与 client auth 用途，不校验 CN。
func (c *CA) VerifyCertificateChain(certPEM []byte) error {
	cert, err := parseLeaf(certPEM)
	if err != nil {
		return err
	}
	return c.verifyChain(cert)
}

func (c *CA) verifyChain(cert *x509.Certificate) error {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return fmt.Errorf("chain verify: %w", err)
	}
	return nil
}

func generateCA(dir, certPath, keyPath string) (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate root key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "GuGuManager Root CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caTTL),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create root certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse root certificate: %w", err)
	}

	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		return nil, fmt.Errorf("write root key: %w", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, fmt.Errorf("write root certificate: %w", err)
	}
	return &CA{cert: cert, key: key, dir: dir}, nil
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseLeaf(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse ca certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("ca certificate missing CA constraint")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "RSA PRIVATE KEY" {
		return nil, errors.New("no RSA PRIVATE KEY block in ca key")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca key: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.Cmp(key.PublicKey.N) != 0 {
		return nil, errors.New("ca key does not match ca certificate")
	}
	return &CA{cert: cert, key: key}, nil
}

func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no CERTIFICATE block in pem")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}
