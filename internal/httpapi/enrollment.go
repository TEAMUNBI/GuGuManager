package httpapi

import (
	"net/http"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// issueAgentEnrollmentTokenRequest 是颁发一次性 Agent 注册令牌的请求体。
// ttlSeconds 省略时使用 store 默认有效期（24h）；显式值必须在 1 秒至 7 天之间。
type issueAgentEnrollmentTokenRequest struct {
	NodeNameHint string `json:"nodeNameHint"`
	TTLSeconds   *int64 `json:"ttlSeconds"`
}

type issueAgentEnrollmentTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// issueAgentEnrollmentToken 为平台管理员颁发一枚一次性注册令牌：明文只
// 在此响应中出现一次，服务端仅保存摘要；令牌单次消费、短期有效。
func (h *Handler) issueAgentEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	var input issueAgentEnrollmentTokenRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &input); err != nil {
			h.writeError(w, traceID(r), err)
			return
		}
	}
	var ttl time.Duration
	if input.TTLSeconds != nil {
		if *input.TTLSeconds < 1 || *input.TTLSeconds > 604800 {
			h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "注册令牌有效期必须在 1 秒至 7 天之间", false))
			return
		}
		ttl = time.Duration(*input.TTLSeconds) * time.Second
	}
	token, expiresAt, err := h.service.IssueAgentEnrollmentToken(input.NodeNameHint, ttl, principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusCreated, issueAgentEnrollmentTokenResponse{
		Token:     token,
		ExpiresAt: expiresAt.UTC(),
	})
}

// revokeNode 立即吊销节点：其 Connect 流在下一个心跳被断开，重新连接被
// 拒绝，正在运行的租约随对账过期回收。
func (h *Handler) revokeNode(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	if err := h.service.RevokeNode(r.PathValue("nodeID"), principalFrom(r).Session.User); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
