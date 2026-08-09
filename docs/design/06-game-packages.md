# 06 游戏包规范

## 1. 两级模型

GameDefinition 是声明式 YAML/JSON 包，覆盖镜像、命令、端口、变量映射、数据目录、健康检查和生命周期。GameExtension 只用于声明式能力无法表达的版本解析、协议查询或迁移。

阶段 1 与 MVP 参考包必须完全声明式，不依赖阶段 3 才提供的 Extension ABI。

## 2. 生产目标的最小可执行结构

以下示例说明生产目标结构。示例中的 registry 是文档保留域，不是可拉取制品，因此该片段不能直接启动服务器；正式包必须使用真实、可访问、经过签名验证的制品和摘要。

```yaml
apiVersion: gugumanager.io/games/v1alpha1
kind: GameDefinition
metadata:
  id: io.gugumanager.papermc
  name: PaperMC
  version: 1.0.0
  license: Apache-2.0
spec:
  release:
    version: "1.21.8"
  compatibility:
    panel: ">=0.1 <1.0"
    agent: ">=0.1 <1.0"
    platforms: [linux/amd64, linux/arm64]
  capabilities: [console, query, backup, update]
  variables:
    schema:
      type: object
      required: [accept_eula, memory_mb]
      properties:
        accept_eula: {type: boolean, const: true}
        memory_mb: {type: integer, minimum: 1024, maximum: 32768}
    bindings:
      - variable: memory_mb
        target: argument
        template: "-Xmx{{ value }}M"
      - variable: accept_eula
        target: file
        path: eula.txt
        template: "eula={{ value }}\n"
  runtime:
    adapter: container/v1
    image: registry.invalid/gugumanager/papermc@sha256:1111111111111111111111111111111111111111111111111111111111111111
    user: "1000:1000"
    workingDir: /srv/game
    command:
      executable: java
      args: ["{{ memory_mb }}", "-jar", "paper.jar", "--nogui"]
    dataMounts:
      - name: server-data
        target: /srv/game
        backup: true
    ports:
      - name: game
        protocol: tcp
        containerPort: 25565
        role: primary
    stop:
      method: console
      value: stop
      timeoutSeconds: 30
    health:
      type: tcp
      portRef: game
      intervalSeconds: 10
      timeoutSeconds: 5
      failureThreshold: 6
  install:
    image: registry.invalid/gugumanager/installers/http@sha256:2222222222222222222222222222222222222222222222222222222222222222
    artifacts:
      - url: https://downloads.example.invalid/paper.jar
        destination: paper.jar
        sha256: 3333333333333333333333333333333333333333333333333333333333333333
  lifecycle:
    install: builtin.artifacts
    configure: builtin.bindings
    update: builtin.replace-artifacts
```

变量绑定只能写入允许的环境变量、参数位置或服务器数据目录内的模板文件。命令始终以 executable 和 args 数组传递，不使用 Shell 拼接。

当前 `argument` 绑定使用精确的 `{{ variable_name }}` 参数作为替换槽位，再用 binding `template` 中的 `{{ value }}` 渲染最终单个参数；例如上面的 `memory_mb` 最终成为 `-Xmx2048M`。该约定仍是 v1alpha1 静态契约，不允许把多个参数或 Shell 片段拼成一个字符串。

### 2.1 启动变量的可执行 Schema 子集

`variables.schema` 不是任意 Draft 2020-12 Schema。GuGuManager 把它解释为 closed object：更新请求只能包含 `properties` 已声明的键；即使清单没有显式写 `additionalProperties: false`，未声明键也会被拒绝。允许的 property 关键字如下：

| 类型 | 允许的关键字 | 运行语义 |
| --- | --- | --- |
| `string` | `default`、`const`、非空且唯一的字符串 `enum`、`minLength`、`maxLength` | 长度按 Unicode code point 计算 |
| `integer` | `default`、`const`、`minimum`、`maximum` | 只允许 `-9007199254740991` 至 `9007199254740991` 的 JavaScript 安全整数 |
| `boolean` | `default`、`const` | 只接受 JSON boolean |

`default` 在这里不是普通 JSON Schema annotation：创建服务器时，Control Plane 会把它物化为初始已配置值。`const` 会约束后续每次更新；同时声明 default 与 const 时两者必须相同，并且二者都必须满足类型、范围、长度和 enum。`required` 引用的变量在 Start/Restart 之前必须已有值；缺值会在创建 operation 或改变 power/generation 前被拒绝。

Secret 只能声明类型以及适用的长度或数值范围，禁止在可分发 Bundle 中写入 `default`、`const` 或 `enum`。Startup API 对 Secret 只返回声明状态和 `hasValue`，无条件省略 `value`、`default`、`constValue` 和 `enumValues`。Secret 的真实值仍不能出现在错误、命令参数或审计负载中。

`number`、`array`、`object`、union type，以及 `pattern`、`format`、`multipleOf`、exclusive bounds、`$ref`、`oneOf`/`anyOf`/`allOf`、条件和依赖等关键字当前不受支持。lint 会直接拒绝它们，不会把运行时无法执行的约束静默忽略。

浏览器 Mock 会对已导入的 Bundle 对象再次执行同一结构、约束、安全整数和绑定路径防线，但 JavaScript 在导入 JSON 时已经把 number 转为 binary64，无法恢复原始十进制词法。因此，Mock 只证明运行时对象与可执行子集兼容；高精度数字是否原本就是数学整数，必须由 CI 和发布流程对原始 Bundle 执行 `gamectl lint` 来证明。不得把 Mock 的二次校验描述为原始 JSON 的逐字节等价验证。

## 3. 生命周期 Hook

支持的逻辑阶段为：`validate`、`resolveRelease`、`install`、`configure`、`preStart`、`postStart`、`queryStatus`、`preStop`、`postStop`、`update`、`backupPrepare`、`backupComplete`、`restoreValidate`、`postRestore` 和 `preDelete`。

每次调用携带 operation ID、幂等键、Bundle 摘要、当前与目标上游游戏版本、定义版本、资源、端口和已授权 Secret 句柄。Hook 只返回进度、检查点、状态条件和结构化输出；超时、重试和状态迁移由编排器控制。

## 4. 版本和发布

- `apiVersion`：Schema 版本。
- `metadata.version`：GameDefinition 自身的严格 SemVer；内容变化必须提升该版本。
- `spec.release.version`：清单声明且固定的上游游戏发行标识，是长度 1 至 128 的不透明字符串，不要求符合 SemVer，但已发布值不得为 `latest`；`resolveRelease` 只能验证或解析到该固定值，不能让已发布 Bundle 随 `latest` 漂移。
- `extensionAbi`：仅在使用扩展时出现。
- Agent 协议版本：Control Plane 与 Agent 通信版本。
- Bundle 摘要：定义、模板、扩展和来源元数据的不可变 `sha256` 身份。

一条目录记录绑定一个上游游戏版本。`GameDefinition.version` 映射自 `metadata.version`，API `gameVersion` 映射自 `spec.release.version`；创建时服务端从同一条已审核目录记录快照两个版本和摘要。创建请求的版本字段只提交定义 ID 与摘要，其他名称、节点和资源字段仍按 API 契约提交；客户端不能提交或推断版本字段。

Bundle 摘要由发布制品计算，不写入被摘要的清单，避免循环定义；它也不同于 `runtime.image` 摘要和 `install.artifacts[].sha256`，后两者只是 Bundle 内引用组件的摘要。已发布的 `(metadata.id, metadata.version)` 不得重新绑定到另一个 Bundle 摘要，修订内容必须提升定义版本。

服务器固定到 Bundle 摘要。升级顺序是：兼容检查、dry-run、备份或快照、迁移、健康检查、提交或回滚。

正式 Bundle 以 OCI Artifact 或等价不可变制品发布，包含签名、SBOM、来源、许可证和依赖。社区包默认需要管理员审核；CI 不能在持有生产凭据的共享 Runner 上执行不可信安装器。

## 5. 当前校验能力

当前 `gamectl lint` 只接受 JSON，并执行以下校验：

- JSON 只能包含一个完整值，拒绝尾随的第二个 JSON 值。
- 使用内嵌的 Draft 2020-12 `v1alpha1.schema.json` 校验必填字段、上游发行版本、基础类型、枚举、范围、各层未知字段、镜像摘要、变量绑定结构和安装 Artifact 基础结构。
- 把 `variables.schema` 限定为第 2.1 节的可执行 closed-object 子集；检查范围/长度关系、default/const 的类型和约束、二者一致性、enum 唯一性以及安全整数域。CLI 与 Store 复用同一语义校验器。
- 检查 Runtime 端口名称不重复。
- 对非 `process` 健康检查，检查 `runtime.health.portRef` 引用已声明的端口；`process` 检查不要求端口引用。
- 检查 `variables.secrets` 中的每个键都存在于 `variables.schema.properties`，Secret 属性不得声明 `default`、`const` 或 `enum`；`variables.schema.required` 中的键也必须已声明且不得重复。
- 每个 variable binding 必须引用已声明变量；`argument` binding 必须在 `runtime.command.args` 有精确变量槽位，Secret 不得进入返回给客户端的命令参数。
- `file` binding 的 `path` 与安装 Artifact 的 `destination` 必须是非空、规范 `/` 表示的可移植相对文件路径；拒绝父目录、绝对路径、Windows 盘符/UNC、反斜杠、NUL、`.`、非规范分隔符和超长路径。多个 Artifact 不得写入同一 destination。

`gamectl init` 生成能够通过当前 Schema 和上述语义检查的 JSON 起始文件。文件中的 `registry.invalid` 仍是保留域，生成成功不表示镜像可拉取、游戏可安装或 Bundle 可发布。

当前 Schema 与 lint 尚未验证模板转义、Artifact 是否可下载且内容匹配摘要、URL/redirect/DNS/网络 allowlist、数据挂载重叠、生命周期实现是否存在等运行语义；`lint` 也尚未解析 YAML。因而“通过 lint”只表示符合当前 v1alpha1 结构、静态目标路径安全和明确列出的变量/端口跨字段约束，不等同于生产一致性测试、安全审核或可运行证明。

Startup 的开发适配器只接受服务器固定 `gameBundleDigest` 对应的内嵌 Bundle。命令、变量类型、默认值、必填性和 Secret 身份均从该不可变定义派生，不能按 Game ID 手写或由请求方改变。Store 会再次验证同一变量子集，防止绕过 CLI 的旧包或篡改包延迟到运行时才静默降级。当前适配器把覆写值放在进程内存；Start/Restart 会先拒绝缺少 required 值的服务器，读取 Secret 时只返回 `hasValue` 状态而不返回候选元数据。这为 UI 和契约测试提供稳定输入，但不是 Bundle 安装、Agent 投递或生产 Secret 存储。

## 6. 目标语义校验

生产 Bundle 门禁还必须增加：

- 验证镜像、安装器与扩展的完整 sha256 摘要、签名、来源和可访问性，不允许 tag 漂移。
- 验证 `portRef` 对应端口的协议与健康检查类型兼容。
- 扩展变量能力前先升级 canonical 子集、共享语义校验、Store/Mock parity 和兼容策略；另行验证绑定目标、模板转义规则和 EULA 要求。
- 验证数据挂载不重叠、备份范围、UID/GID 和最小权限。
- 验证 Artifact URL、摘要、目标路径、大小限制和网络 allowlist。
- 验证生命周期实现存在，扩展权限只引用已声明能力。
- 在隔离环境执行安装、配置、启动、健康、停止、更新、备份、恢复和重复投递一致性测试。

这些目标在对应 Schema、Runner 和一致性测试工具实现前不得写入 CI 成功声明。
