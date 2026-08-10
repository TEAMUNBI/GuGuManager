FROM node:24.18.0-alpine3.23 AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY spec/ ./spec/
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/gugu-control-plane ./cmd/control-plane \
    && go build -trimpath -ldflags="-s -w" -o /out/gugu-agent ./cmd/agent

FROM alpine:3.23
RUN addgroup -S -g 10001 gugu \
    && adduser -S -D -H -u 10001 -G gugu gugu \
    && mkdir -p /var/lib/gugumanager/servers \
    && chown -R 10001:10001 /var/lib/gugumanager
WORKDIR /app
COPY --from=go-build /out/gugu-control-plane /usr/local/bin/gugu-control-plane
COPY --from=go-build /out/gugu-agent /usr/local/bin/gugu-agent
COPY --from=web-build /src/web/dist /app/web
LABEL org.opencontainers.image.title="GuGuManager Control Plane" \
    org.opencontainers.image.description="Fail-closed preview image; the production adapter is not implemented yet"
ENV GUGU_ENVIRONMENT=production \
    GUGU_HTTP_ADDR=0.0.0.0:8080 \
    GUGU_WEB_ROOT=/app/web
USER 10001:10001
EXPOSE 8080 8443
# 默认入口运行 control-plane；运行 agent 时由 compose 的 entrypoint 覆盖
# 为 ["/usr/local/bin/gugu-agent"]（两个二进制均由 go-build 阶段产出）。
ENTRYPOINT ["/usr/local/bin/gugu-control-plane"]
