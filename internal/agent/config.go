// Package agent 实现 GuGuManager 的 Node Agent：
// 出站 mTLS gRPC（Enroll + Connect 双向流）与 Docker 任务执行器。
package agent

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Config 是 Agent 的运行时配置，来自环境变量 GUGU_AGENT_*（含旧变量兼容回退）。
type Config struct {
	PanelAddr         string // gRPC 地址，如 "127.0.0.1:8443"
	RegistrationToken string // 首次注册令牌（一次性令牌；预置 CA 必需）
	NodeName          string // 节点名称（Enroll 与 CSR CN）
	AgentVersion      string // Agent 版本号
	DataRoot          string // 服务器数据根（本机服务器文件存储位置，含操作日志）
	CertDir           string // 证书持久化目录
	TrustRootPath     string // CA 根证书文件路径（部署时预置；注册与连接均强制校验）
	CAFingerprint     string // 可选：服务器证书链（叶子或 CA 根）的 SHA-256 指纹钉扎
}

// LoadConfig 从环境变量读取配置。优先读取 GUGU_AGENT_* 前缀变量，
// 缺失时回退到旧变量（GUGU_PANEL_URL / GUGU_NODE_NAME / GUGU_AGENT_TOKEN），
// 再回退到默认值。
func LoadConfig() (Config, error) {
	dataRoot := firstEnv("GUGU_AGENT_DATA_ROOT", "data")
	certDir := firstEnv("GUGU_AGENT_CERT_DIR", filepath.Join(dataRoot, "certs"))
	panel := firstEnv("GUGU_AGENT_PANEL_ADDR", "GUGU_PANEL_URL", "127.0.0.1:8443")

	return Config{
		PanelAddr:         normalizeAddr(panel),
		RegistrationToken: firstEnv("GUGU_AGENT_REGISTRATION_TOKEN", "GUGU_REGISTRATION_TOKEN", strings.TrimSpace(os.Getenv("GUGU_AGENT_TOKEN"))),
		NodeName:          firstEnv("GUGU_AGENT_NODE_NAME", "GUGU_NODE_NAME", "nimbus-east-01"),
		AgentVersion:      firstEnv("GUGU_AGENT_VERSION", "0.1.0-dev"),
		DataRoot:          dataRoot,
		CertDir:           certDir,
		TrustRootPath:     firstEnv("GUGU_AGENT_TRUST_ROOT", filepath.Join(certDir, "ca.crt")),
		CAFingerprint:     firstEnv("GUGU_AGENT_CA_FINGERPRINT", ""),
	}, nil
}

// firstEnv 依次返回第一个非空环境变量的值，全部为空时返回 fallback。
func firstEnv(keys ...string) string {
	for i := 0; i < len(keys)-1; i++ {
		if v := strings.TrimSpace(os.Getenv(keys[i])); v != "" {
			return v
		}
	}
	return keys[len(keys)-1]
}

// normalizeAddr 把旧版 GUGU_PANEL_URL 形式的 URL（http://host:port）规范为
// host:port 的 gRPC 地址；已是 host:port 的地址原样返回。
func normalizeAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return raw
}
