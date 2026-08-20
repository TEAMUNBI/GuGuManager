# GuGuManager Extension Runner

独立 Rust/Wasmtime 进程，承载 `wasi-component/v1` 扩展。Agent 与 Runner 使用
4 字节大端长度 + Protobuf v1 帧通信；单帧上限 16 MiB。Runner 默认不挂载
服务器目录、不开放 HTTPS、不注入 Secret，只有 Invoke 明确声明的权限才会进入
对应 Host API。

安全边界包括：组件 SHA-256 复验、无环境继承、fuel、epoch 超时、线性内存、
实例/表、输出和 HTTP 响应限制；目录访问通过 capability directory，HTTPS 仅允许
allowlist 域名且连接前拒绝私网/回环/链路本地解析，重定向关闭。Secret 只能作为
一次 HTTPS 请求的绑定使用，Wasm 组件没有读取明文的 Host API。

构建要求 Rust 1.93+：

```bash
cargo test --manifest-path extension-runner/Cargo.toml
cargo build --release --locked --manifest-path extension-runner/Cargo.toml
```

Wasmtime 固定到 48 系列 LTS；升级必须重新执行恶意组件、资源耗尽、路径逃逸、
SSRF 和 ABI 兼容测试。
