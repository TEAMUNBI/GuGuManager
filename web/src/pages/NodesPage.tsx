import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Activity, CircleAlert, Clipboard, Cpu, HardDrive, KeyRound, MemoryStick, RefreshCw, TerminalSquare, Trash2 } from "lucide-react";
import { useAppContext } from "../app/App";
import { Modal } from "../components/Modal";
import { api } from "../lib/api";
import type { AgentEnrollmentToken, Node, NodeCondition } from "../lib/types";
import { formatBytes, formatDateTime, relativeTime } from "../lib/format";
import { LoadingState, ErrorState } from "../components/PageState";
import { StatusBadge, toneForNode } from "../components/StatusBadge";
import { type LocalizedCopy, useCopy, useI18n } from "../i18n/I18n";

interface NodesCopy {
  page: {
    eyebrow: string;
    title: string;
    description: string;
    syncing: string;
    snapshot: string;
    refresh: string;
    loading: string;
    loadError: string;
    registry: string;
    registeredNodes: string;
    issueToken: string;
  };
  kpi: {
    available: string;
    registered: (count: number) => string;
    reservedMemory: string;
    reportedWorkloads: string;
    latestSnapshot: string;
    capacity: (value: string) => string;
  };
  table: {
    node: string;
    condition: string;
    agent: string;
    heartbeat: string;
    servers: string;
    capacity: string;
    actions: string;
    revoke: (nodeName: string) => string;
  };
  card: {
    memory: string;
    disk: string;
    compute: string;
    cores: (count: number) => string;
    runningServers: (count: number) => string;
    lastHeartbeat: (value: string) => string;
    capabilities: (count: number) => string;
  };
  enrollment: {
    title: string;
    description: string;
    nodeName: string;
    nodeNamePlaceholder: string;
    nodeNameHelp: string;
    ttl: string;
    ttlHelp: string;
    cancel: string;
    issue: string;
    issuing: string;
    validation: string;
    issueError: string;
    resultTitle: string;
    resultDescription: string;
    token: string;
    expires: string;
    copy: string;
    done: string;
    copied: string;
    clipboardUnavailable: string;
    issued: string;
  };
  revoke: {
    title: string;
    description: (nodeName: string) => string;
    warning: string;
    cancel: string;
    confirm: string;
    revoking: string;
    success: (nodeName: string) => string;
    error: string;
  };
  conditions: Record<NodeCondition, string>;
}

const nodesCopy: LocalizedCopy<NodesCopy> = {
  "zh-CN": {
    page: {
      eyebrow: "基础设施 / 节点",
      title: "节点",
      description: "查看各节点的在线状态、资源容量和服务器负载。",
      syncing: "同步中",
      snapshot: "状态快照",
      refresh: "刷新",
      loading: "正在读取节点状态",
      loadError: "无法加载节点",
      registry: "节点列表",
      registeredNodes: "已注册节点",
      issueToken: "颁发注册令牌",
    },
    kpi: {
      available: "在线节点",
      registered: (count) => `共 ${count} 个节点`,
      reservedMemory: "已分配内存",
      reportedWorkloads: "运行中的服务器",
      latestSnapshot: "全部节点合计",
      capacity: (value) => `总计 ${value}`,
    },
    table: { node: "节点", condition: "状态", agent: "Agent 版本", heartbeat: "最后心跳", servers: "服务器", capacity: "内存占用", actions: "操作", revoke: (nodeName) => `撤销节点 ${nodeName}` },
    card: {
      memory: "内存",
      disk: "磁盘",
      compute: "计算资源",
      cores: (count) => `${count} 核心`,
      runningServers: (count) => `${count} 台运行中的服务器`,
      lastHeartbeat: (value) => `上次心跳 ${value}`,
      capabilities: (count) => `${count} 项运行能力`,
    },
    enrollment: {
      title: "颁发 Agent 注册令牌",
      description: "创建一个短期、仅可使用一次的注册令牌。明文关闭后无法再次查看。",
      nodeName: "节点名称提示（可选）",
      nodeNamePlaceholder: "例如：shanghai-edge-01",
      nodeNameHelp: "最多 100 个字符，仅用于记录计划注册的节点。",
      ttl: "有效期（秒）",
      ttlHelp: "允许 1 至 604800 秒，默认 86400 秒。",
      cancel: "取消",
      issue: "颁发令牌",
      issuing: "正在颁发",
      validation: "有效期必须是 1 至 604800 之间的整数。",
      issueError: "无法颁发注册令牌",
      resultTitle: "注册令牌已颁发",
      resultDescription: "请立即将令牌安全地交给 Agent。关闭后无法恢复明文。",
      token: "一次性令牌",
      expires: "过期时间",
      copy: "复制",
      done: "完成",
      copied: "注册令牌已复制",
      clipboardUnavailable: "无法访问剪贴板，请手动复制令牌",
      issued: "注册令牌已颁发",
    },
    revoke: {
      title: "撤销节点",
      description: (nodeName) => `撤销 ${nodeName} 后，该节点将在下次心跳断开且无法重新连接。`,
      warning: "此操作不会自动迁移正在运行的服务器。",
      cancel: "取消",
      confirm: "确认撤销",
      revoking: "正在撤销",
      success: (nodeName) => `节点 ${nodeName} 已撤销`,
      error: "无法撤销节点",
    },
    conditions: { available: "在线", offline: "离线", maintenance: "维护中" },
  },
  en: {
    page: {
      eyebrow: "INFRASTRUCTURE / NODES",
      title: "Nodes",
      description: "Monitor node availability, resource capacity, and server workload.",
      syncing: "Syncing",
      snapshot: "Snapshot",
      refresh: "Refresh",
      loading: "Reading node registry",
      loadError: "Unable to load nodes",
      registry: "REGISTRY",
      registeredNodes: "Registered nodes",
      issueToken: "Issue enrollment token",
    },
    kpi: {
      available: "Available nodes",
      registered: (count) => `${count} registered`,
      reservedMemory: "Allocated memory",
      reportedWorkloads: "Running servers",
      latestSnapshot: "Across all nodes",
      capacity: (value) => `of ${value} total`,
    },
    table: { node: "Node", condition: "Condition", agent: "Agent version", heartbeat: "Last heartbeat", servers: "Servers", capacity: "Memory usage", actions: "Actions", revoke: (nodeName) => `Revoke node ${nodeName}` },
    card: {
      memory: "Memory",
      disk: "Disk",
      compute: "Compute",
      cores: (count) => `${count} ${count === 1 ? "core" : "cores"}`,
      runningServers: (count) => `${count} running ${count === 1 ? "server" : "servers"}`,
      lastHeartbeat: (value) => `Last heartbeat ${value}`,
      capabilities: (count) => `${count} ${count === 1 ? "capability" : "capabilities"}`,
    },
    enrollment: {
      title: "Issue Agent enrollment token",
      description: "Create a short-lived, single-use enrollment token. Its plaintext cannot be viewed again after closing.",
      nodeName: "Node name hint (optional)",
      nodeNamePlaceholder: "For example: shanghai-edge-01",
      nodeNameHelp: "Up to 100 characters, used only to record the intended node.",
      ttl: "Lifetime (seconds)",
      ttlHelp: "From 1 to 604800 seconds; the default is 86400.",
      cancel: "Cancel",
      issue: "Issue token",
      issuing: "Issuing",
      validation: "Lifetime must be an integer from 1 to 604800.",
      issueError: "Unable to issue enrollment token",
      resultTitle: "Enrollment token issued",
      resultDescription: "Give this token to the Agent through a secure channel now. Its plaintext cannot be recovered after closing.",
      token: "One-time token",
      expires: "Expires",
      copy: "Copy",
      done: "Done",
      copied: "Enrollment token copied",
      clipboardUnavailable: "Clipboard access is unavailable; copy the token manually",
      issued: "Enrollment token issued",
    },
    revoke: {
      title: "Revoke node",
      description: (nodeName) => `After ${nodeName} is revoked, it will disconnect at its next heartbeat and cannot reconnect.`,
      warning: "This action does not migrate running servers automatically.",
      cancel: "Cancel",
      confirm: "Revoke node",
      revoking: "Revoking",
      success: (nodeName) => `Node ${nodeName} revoked`,
      error: "Unable to revoke node",
    },
    conditions: { available: "Available", offline: "Offline", maintenance: "Maintenance" },
  },
  ja: {
    page: {
      eyebrow: "インフラ / ノード",
      title: "ノード",
      description: "各ノードの稼働状況、リソース容量、サーバー負荷を確認できます。",
      syncing: "同期中",
      snapshot: "スナップショット",
      refresh: "更新",
      loading: "ノードレジストリを読み込み中",
      loadError: "ノードを読み込めません",
      registry: "ノード一覧",
      registeredNodes: "登録済みノード",
      issueToken: "登録トークンを発行",
    },
    kpi: {
      available: "利用可能なノード",
      registered: (count) => `全 ${count} 台`,
      reservedMemory: "割り当て済みメモリ",
      reportedWorkloads: "稼働中のサーバー",
      latestSnapshot: "全ノードの合計",
      capacity: (value) => `全体 ${value}`,
    },
    table: { node: "ノード", condition: "状態", agent: "エージェントバージョン", heartbeat: "最終ハートビート", servers: "サーバー", capacity: "メモリ使用率", actions: "操作", revoke: (nodeName) => `ノード ${nodeName} を失効` },
    card: {
      memory: "メモリ",
      disk: "ディスク",
      compute: "計算リソース",
      cores: (count) => `${count} コア`,
      runningServers: (count) => `${count} 台が稼働中`,
      lastHeartbeat: (value) => `最終ハートビート ${value}`,
      capabilities: (count) => `${count} 個の機能`,
    },
    enrollment: {
      title: "Agent 登録トークンを発行",
      description: "有効期間が短く、一度だけ使用できる登録トークンを作成します。閉じると平文は再表示できません。",
      nodeName: "ノード名のヒント（任意）",
      nodeNamePlaceholder: "例: shanghai-edge-01",
      nodeNameHelp: "最大 100 文字。登録予定のノードを記録するためだけに使用します。",
      ttl: "有効期間（秒）",
      ttlHelp: "1～604800 秒。既定値は 86400 秒です。",
      cancel: "キャンセル",
      issue: "トークンを発行",
      issuing: "発行中",
      validation: "有効期間は 1～604800 の整数で入力してください。",
      issueError: "登録トークンを発行できません",
      resultTitle: "登録トークンを発行しました",
      resultDescription: "今すぐ安全な経路で Agent に渡してください。閉じると平文は復元できません。",
      token: "ワンタイムトークン",
      expires: "有効期限",
      copy: "コピー",
      done: "完了",
      copied: "登録トークンをコピーしました",
      clipboardUnavailable: "クリップボードを利用できません。手動でコピーしてください",
      issued: "登録トークンを発行しました",
    },
    revoke: {
      title: "ノードを失効",
      description: (nodeName) => `${nodeName} を失効すると、次回のハートビートで切断され、再接続できなくなります。`,
      warning: "実行中のサーバーは自動的に移行されません。",
      cancel: "キャンセル",
      confirm: "失効する",
      revoking: "失効中",
      success: (nodeName) => `ノード ${nodeName} を失効しました`,
      error: "ノードを失効できません",
    },
    conditions: { available: "利用可能", offline: "オフライン", maintenance: "メンテナンス中" },
  },
  ko: {
    page: {
      eyebrow: "인프라 / 노드",
      title: "노드",
      description: "각 노드의 가동 상태, 리소스 용량, 서버 부하를 확인합니다.",
      syncing: "동기화 중",
      snapshot: "스냅샷",
      refresh: "새로 고침",
      loading: "노드 레지스트리를 읽는 중",
      loadError: "노드를 불러올 수 없습니다",
      registry: "노드 목록",
      registeredNodes: "등록된 노드",
      issueToken: "등록 토큰 발급",
    },
    kpi: {
      available: "사용 가능한 노드",
      registered: (count) => `총 ${count}개`,
      reservedMemory: "할당된 메모리",
      reportedWorkloads: "실행 중인 서버",
      latestSnapshot: "전체 노드 합계",
      capacity: (value) => `전체 ${value}`,
    },
    table: { node: "노드", condition: "상태", agent: "에이전트 버전", heartbeat: "마지막 하트비트", servers: "서버", capacity: "메모리 사용률", actions: "작업", revoke: (nodeName) => `노드 ${nodeName} 해지` },
    card: {
      memory: "메모리",
      disk: "디스크",
      compute: "컴퓨팅 리소스",
      cores: (count) => `${count}코어`,
      runningServers: (count) => `${count}개 서버 실행 중`,
      lastHeartbeat: (value) => `마지막 하트비트 ${value}`,
      capabilities: (count) => `기능 ${count}개`,
    },
    enrollment: {
      title: "Agent 등록 토큰 발급",
      description: "유효 기간이 짧고 한 번만 사용할 수 있는 등록 토큰을 만듭니다. 닫은 후에는 평문을 다시 볼 수 없습니다.",
      nodeName: "노드 이름 힌트(선택)",
      nodeNamePlaceholder: "예: shanghai-edge-01",
      nodeNameHelp: "최대 100자이며 등록 예정 노드를 기록하는 용도로만 사용합니다.",
      ttl: "유효 기간(초)",
      ttlHelp: "1~604800초, 기본값은 86400초입니다.",
      cancel: "취소",
      issue: "토큰 발급",
      issuing: "발급 중",
      validation: "유효 기간은 1~604800 사이의 정수여야 합니다.",
      issueError: "등록 토큰을 발급할 수 없습니다",
      resultTitle: "등록 토큰이 발급되었습니다",
      resultDescription: "지금 안전한 경로로 Agent에 전달하세요. 닫은 후에는 평문을 복구할 수 없습니다.",
      token: "일회용 토큰",
      expires: "만료 시간",
      copy: "복사",
      done: "완료",
      copied: "등록 토큰을 복사했습니다",
      clipboardUnavailable: "클립보드를 사용할 수 없습니다. 토큰을 직접 복사하세요",
      issued: "등록 토큰이 발급되었습니다",
    },
    revoke: {
      title: "노드 해지",
      description: (nodeName) => `${nodeName}을(를) 해지하면 다음 하트비트에 연결이 끊기고 다시 연결할 수 없습니다.`,
      warning: "실행 중인 서버는 자동으로 이전되지 않습니다.",
      cancel: "취소",
      confirm: "노드 해지",
      revoking: "해지 중",
      success: (nodeName) => `노드 ${nodeName}이(가) 해지되었습니다`,
      error: "노드를 해지할 수 없습니다",
    },
    conditions: { available: "사용 가능", offline: "오프라인", maintenance: "유지 관리" },
  },
};

export function NodesPage() {
  const copy = useCopy(nodesCopy);
  const { locale } = useI18n();
  const { session, toast } = useAppContext();
  const [nodes, setNodes] = useState<Node[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [enrollmentOpen, setEnrollmentOpen] = useState(false);
  const [nodeNameHint, setNodeNameHint] = useState("");
  const [ttlSeconds, setTtlSeconds] = useState("86400");
  const [enrollmentBusy, setEnrollmentBusy] = useState(false);
  const [enrollmentError, setEnrollmentError] = useState("");
  const [issuedToken, setIssuedToken] = useState<AgentEnrollmentToken | null>(null);
  const [pendingRevoke, setPendingRevoke] = useState<Node | null>(null);
  const [revokeBusy, setRevokeBusy] = useState(false);
  const [revokeError, setRevokeError] = useState("");
  const load = () => {
    setLoading(true);
    setError("");
    api.nodes()
      .then(setNodes)
      .catch((reason) => setError(reason instanceof Error ? reason.message : copy.page.loadError))
      .finally(() => setLoading(false));
  };

  useEffect(() => { load(); }, []);

  const openEnrollment = () => {
    setEnrollmentError("");
    setNodeNameHint("");
    setTtlSeconds("86400");
    setEnrollmentOpen(true);
  };

  const closeEnrollment = () => {
    if (enrollmentBusy) return;
    setEnrollmentOpen(false);
    setEnrollmentError("");
  };

  const issueEnrollmentToken = async (event: FormEvent) => {
    event.preventDefault();
    const ttl = Number(ttlSeconds);
    if (!Number.isInteger(ttl) || ttl < 1 || ttl > 604_800) {
      setEnrollmentError(copy.enrollment.validation);
      return;
    }
    setEnrollmentBusy(true);
    setEnrollmentError("");
    try {
      const issued = await api.issueAgentEnrollmentToken({
        ...(nodeNameHint.trim() ? { nodeNameHint: nodeNameHint.trim() } : {}),
        ttlSeconds: ttl,
      }, session.csrfToken);
      setEnrollmentOpen(false);
      setIssuedToken(issued);
      toast(copy.enrollment.issued);
    } catch (reason) {
      setEnrollmentError(messageFor(reason, copy.enrollment.issueError));
    } finally {
      setEnrollmentBusy(false);
    }
  };

  const copyEnrollmentToken = async () => {
    if (!issuedToken) return;
    try {
      await navigator.clipboard.writeText(issuedToken.token);
      toast(copy.enrollment.copied);
    } catch {
      toast(copy.enrollment.clipboardUnavailable, "warning");
    }
  };

  const requestRevoke = (node: Node) => {
    setRevokeError("");
    setPendingRevoke(node);
  };

  const closeRevoke = () => {
    if (revokeBusy) return;
    setPendingRevoke(null);
    setRevokeError("");
  };

  const revokeNode = async () => {
    const target = pendingRevoke;
    if (!target) return;
    setRevokeBusy(true);
    setRevokeError("");
    try {
      await api.revokeNode(target.id, session.csrfToken);
      setNodes((current) => current.filter((node) => node.id !== target.id));
      setPendingRevoke(null);
      toast(copy.revoke.success(target.name), "warning");
    } catch (reason) {
      setRevokeError(messageFor(reason, copy.revoke.error));
    } finally {
      setRevokeBusy(false);
    }
  };

  if (loading && !nodes.length) return <section className="page"><LoadingState label={copy.page.loading} /></section>;
  if (error && !nodes.length) return <section className="page"><ErrorState message={error} onRetry={load} /></section>;

  const reservedMemory = nodes.reduce((total, node) => total + node.allocatedMemoryBytes, 0);
  const totalMemory = nodes.reduce((total, node) => total + node.memoryBytes, 0);
  const runningServers = nodes.reduce((total, node) => total + node.runningServers, 0);

  return (
    <section className="page nodes-page">
      <div className="page-heading page-heading-wide">
        <div><h1>{copy.page.title}</h1><p className="lede">{copy.page.description}</p></div>
        <div className="heading-actions"><span className="sync-state"><i />{loading ? copy.page.syncing : copy.page.snapshot}</span><button className="button primary" onClick={openEnrollment}><KeyRound size={16} />{copy.page.issueToken}</button><button className="button secondary" onClick={load}><RefreshCw size={16} />{copy.page.refresh}</button></div>
      </div>
      <div className="node-kpi-row">
        <Kpi label={copy.kpi.available} value={`${nodes.filter((node) => node.condition === "available").length}`} detail={copy.kpi.registered(nodes.length)} tone="mint" />
        <Kpi label={copy.kpi.reservedMemory} value={formatBytes(reservedMemory)} detail={copy.kpi.capacity(formatBytes(totalMemory))} tone="blue" />
        <Kpi label={copy.kpi.reportedWorkloads} value={`${runningServers}`} detail={copy.kpi.latestSnapshot} tone="mint" />
      </div>
      <div className="node-card-grid">{nodes.map((node) => <NodeCard key={node.id} node={node} copy={copy} />)}</div>
      <div className="panel node-registry-panel">
        <div className="panel-heading"><div><p className="eyebrow">{copy.page.registry}</p><h2>{copy.page.registeredNodes}</h2></div></div>
        <div className="server-table-wrap"><table className="data-table node-table"><thead><tr><th>{copy.table.node}</th><th>{copy.table.condition}</th><th>{copy.table.agent}</th><th>{copy.table.heartbeat}</th><th>{copy.table.servers}</th><th>{copy.table.capacity}</th><th>{copy.table.actions}</th></tr></thead><tbody>
          {nodes.map((node) => <tr key={node.id}><td><div className="node-cell"><span className={`node-health-dot node-${node.condition}`} /><span translate="no"><strong>{node.name}</strong><small>{node.region} · {node.address}</small></span></div></td><td><StatusBadge tone={toneForNode(node.condition)}>{copy.conditions[node.condition]}</StatusBadge></td><td><code translate="no">{node.version}</code></td><td><span className="muted-text">{relativeTime(node.lastHeartbeatAt)}</span><small className="table-subline">{formatDateTime(node.lastHeartbeatAt)}</small></td><td><strong>{node.runningServers}</strong><span className="muted-text"> / {node.totalServers}</span></td><td><div className="capacity-inline"><div className="mini-progress"><i style={{ width: `${Math.round((node.allocatedMemoryBytes / node.memoryBytes) * 100)}%` }} /></div><span>{Math.round((node.allocatedMemoryBytes / node.memoryBytes) * 100)}%</span></div></td><td><button type="button" className="icon-button danger-button" onClick={() => requestRevoke(node)} aria-label={copy.table.revoke(node.name)} title={copy.table.revoke(node.name)}><Trash2 size={15} /></button></td></tr>)}
        </tbody></table></div>
      </div>
      {error && nodes.length > 0 && <div className="inline-warning" role="alert"><CircleAlert size={15} />{error}</div>}
      <Modal
        open={enrollmentOpen}
        title={copy.enrollment.title}
        description={copy.enrollment.description}
        onClose={closeEnrollment}
        dismissible={!enrollmentBusy}
        footer={<><button type="button" className="button secondary" onClick={closeEnrollment} disabled={enrollmentBusy}>{copy.enrollment.cancel}</button><button type="submit" form="issue-enrollment-token-form" className="button primary" disabled={enrollmentBusy}>{enrollmentBusy ? copy.enrollment.issuing : copy.enrollment.issue}</button></>}
      >
        <form id="issue-enrollment-token-form" className="modal-form" onSubmit={issueEnrollmentToken}>
          <label>{copy.enrollment.nodeName}<input autoFocus maxLength={100} value={nodeNameHint} onChange={(event) => setNodeNameHint(event.target.value)} placeholder={copy.enrollment.nodeNamePlaceholder} disabled={enrollmentBusy} /><small className="field-hint">{copy.enrollment.nodeNameHelp}</small></label>
          <label>{copy.enrollment.ttl}<input type="number" min={1} max={604800} step={1} value={ttlSeconds} onChange={(event) => setTtlSeconds(event.target.value)} disabled={enrollmentBusy} required /><small className="field-hint">{copy.enrollment.ttlHelp}</small></label>
          {enrollmentError && <div className="form-error" role="alert">{enrollmentError}</div>}
        </form>
      </Modal>
      <Modal
        open={Boolean(issuedToken)}
        title={copy.enrollment.resultTitle}
        description={copy.enrollment.resultDescription}
        onClose={() => setIssuedToken(null)}
        footer={<><button type="button" className="button secondary" onClick={() => void copyEnrollmentToken()}><Clipboard size={15} />{copy.enrollment.copy}</button><button type="button" className="button primary" onClick={() => setIssuedToken(null)}>{copy.enrollment.done}</button></>}
      >
        {issuedToken && <div className="reset-token-sheet"><span>{copy.enrollment.token}</span><code translate="no">{issuedToken.token}</code><dl><div><dt>{copy.enrollment.expires}</dt><dd>{new Date(issuedToken.expiresAt).toLocaleString(locale)}</dd></div></dl></div>}
      </Modal>
      <Modal
        open={Boolean(pendingRevoke)}
        title={copy.revoke.title}
        description={pendingRevoke ? copy.revoke.description(pendingRevoke.name) : ""}
        onClose={closeRevoke}
        dismissible={!revokeBusy}
        footer={<><button type="button" className="button secondary" onClick={closeRevoke} disabled={revokeBusy}>{copy.revoke.cancel}</button><button type="button" className="button danger-solid" onClick={() => void revokeNode()} disabled={revokeBusy}>{revokeBusy ? copy.revoke.revoking : copy.revoke.confirm}</button></>}
      >
        {pendingRevoke && <div className="modal-form"><div className="danger-confirm"><CircleAlert size={18} /><div><strong translate="no">{pendingRevoke.name}</strong><span>{copy.revoke.warning}</span></div></div>{revokeError && <div className="form-error" role="alert">{revokeError}</div>}</div>}
      </Modal>
    </section>
  );
}

function Kpi({ label, value, detail, tone }: { label: string; value: string; detail: string; tone: "mint" | "orange" | "blue" }) {
  return <div className={`metric-card metric-${tone}`}><div className="metric-card-top"><span>{label}</span></div><strong>{value}</strong><small>{detail}</small></div>;
}

function NodeCard({ node, copy }: { node: Node; copy: NodesCopy }) {
  const memory = (node.allocatedMemoryBytes / node.memoryBytes) * 100;
  const disk = (node.allocatedDiskBytes / node.diskBytes) * 100;
  return <article className={`node-card node-card-${node.condition}`}><header><div className="node-card-title"><span className={`node-health-dot node-${node.condition}`} /><div translate="no"><h3>{node.name}</h3><span>{node.region}</span></div></div><StatusBadge tone={toneForNode(node.condition)}>{copy.conditions[node.condition]}</StatusBadge></header><div className="node-card-grid-lines"><CapacityLine icon={<MemoryStick size={15} />} label={copy.card.memory} value={`${Math.round(memory)}%`} detail={`${formatBytes(node.allocatedMemoryBytes)} / ${formatBytes(node.memoryBytes)}`} percent={memory} /><CapacityLine icon={<HardDrive size={15} />} label={copy.card.disk} value={`${Math.round(disk)}%`} detail={`${formatBytes(node.allocatedDiskBytes)} / ${formatBytes(node.diskBytes)}`} percent={disk} blue /><CapacityLine icon={<Cpu size={15} />} label={copy.card.compute} value={copy.card.cores(node.cpuCores)} detail={copy.card.runningServers(node.runningServers)} percent={Math.min(100, (node.runningServers / Math.max(node.totalServers, 1)) * 100)} /></div><footer><span><Activity size={14} />{copy.card.lastHeartbeat(relativeTime(node.lastHeartbeatAt))}</span><span><TerminalSquare size={14} />{copy.card.capabilities(node.capabilities.length)}</span></footer></article>;
}

function CapacityLine({ icon, label, value, detail, percent, blue = false }: { icon: ReactNode; label: string; value: string; detail: string; percent: number; blue?: boolean }) {
  return <div className="capacity-line"><div className="capacity-line-head"><span>{icon}{label}</span><strong>{value}</strong></div><div className={`capacity-track${blue ? " track-blue" : ""}`}><i style={{ width: `${Math.min(100, percent)}%` }} /></div><small>{detail}</small></div>;
}

function messageFor(reason: unknown, fallback: string): string {
  return reason instanceof Error ? reason.message : fallback;
}
