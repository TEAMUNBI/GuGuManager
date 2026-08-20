# WASI Component reference extension

最小但真实的 `gugumanager:extension@1.0.0` 组件：调用结构化进度 Host API，
把输入转为大写并返回。它不申请网络、文件或 Secret 权限，用于验证第三方扩展的
签名、ABI 协商、Protobuf 分帧、fuel/内存/超时和输出限制。

```bash
rustup target add wasm32-wasip2
cargo build --release --target wasm32-wasip2 \
  --manifest-path examples/wasi-extension/Cargo.toml
sha256sum examples/wasi-extension/target/wasm32-wasip2/release/gugumanager_wasi_extension_example.wasm
```

发布流水线必须把构建结果作为 Bundle Artifact 固定 SHA-256，再使用受信
Ed25519 根签名 Bundle；仓库不接受由未固定工具链生成后手工覆盖的 wasm 二进制。
