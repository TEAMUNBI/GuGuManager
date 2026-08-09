import { useEffect, useState, type ReactNode } from "react";
import { Activity, Cpu, HardDrive, MemoryStick, RefreshCw, TerminalSquare } from "lucide-react";
import { api } from "../lib/api";
import type { Node, NodeCondition } from "../lib/types";
import { formatBytes, formatDateTime, relativeTime } from "../lib/format";
import { LoadingState, ErrorState } from "../components/PageState";
import { StatusBadge, toneForNode } from "../components/StatusBadge";
import { type LocalizedCopy, useCopy } from "../i18n/I18n";

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
    },
    kpi: {
      available: "在线节点",
      registered: (count) => `共 ${count} 个节点`,
      reservedMemory: "已分配内存",
      reportedWorkloads: "运行中的服务器",
      latestSnapshot: "全部节点合计",
      capacity: (value) => `总计 ${value}`,
    },
    table: { node: "节点", condition: "状态", agent: "Agent 版本", heartbeat: "最后心跳", servers: "服务器", capacity: "内存占用" },
    card: {
      memory: "内存",
      disk: "磁盘",
      compute: "计算资源",
      cores: (count) => `${count} 核心`,
      runningServers: (count) => `${count} 台运行中的服务器`,
      lastHeartbeat: (value) => `上次心跳 ${value}`,
      capabilities: (count) => `${count} 项运行能力`,
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
    },
    kpi: {
      available: "Available nodes",
      registered: (count) => `${count} registered`,
      reservedMemory: "Allocated memory",
      reportedWorkloads: "Running servers",
      latestSnapshot: "Across all nodes",
      capacity: (value) => `of ${value} total`,
    },
    table: { node: "Node", condition: "Condition", agent: "Agent version", heartbeat: "Last heartbeat", servers: "Servers", capacity: "Memory usage" },
    card: {
      memory: "Memory",
      disk: "Disk",
      compute: "Compute",
      cores: (count) => `${count} ${count === 1 ? "core" : "cores"}`,
      runningServers: (count) => `${count} running ${count === 1 ? "server" : "servers"}`,
      lastHeartbeat: (value) => `Last heartbeat ${value}`,
      capabilities: (count) => `${count} ${count === 1 ? "capability" : "capabilities"}`,
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
    },
    kpi: {
      available: "利用可能なノード",
      registered: (count) => `全 ${count} 台`,
      reservedMemory: "割り当て済みメモリ",
      reportedWorkloads: "稼働中のサーバー",
      latestSnapshot: "全ノードの合計",
      capacity: (value) => `全体 ${value}`,
    },
    table: { node: "ノード", condition: "状態", agent: "エージェントバージョン", heartbeat: "最終ハートビート", servers: "サーバー", capacity: "メモリ使用率" },
    card: {
      memory: "メモリ",
      disk: "ディスク",
      compute: "計算リソース",
      cores: (count) => `${count} コア`,
      runningServers: (count) => `${count} 台が稼働中`,
      lastHeartbeat: (value) => `最終ハートビート ${value}`,
      capabilities: (count) => `${count} 個の機能`,
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
    },
    kpi: {
      available: "사용 가능한 노드",
      registered: (count) => `총 ${count}개`,
      reservedMemory: "할당된 메모리",
      reportedWorkloads: "실행 중인 서버",
      latestSnapshot: "전체 노드 합계",
      capacity: (value) => `전체 ${value}`,
    },
    table: { node: "노드", condition: "상태", agent: "에이전트 버전", heartbeat: "마지막 하트비트", servers: "서버", capacity: "메모리 사용률" },
    card: {
      memory: "메모리",
      disk: "디스크",
      compute: "컴퓨팅 리소스",
      cores: (count) => `${count}코어`,
      runningServers: (count) => `${count}개 서버 실행 중`,
      lastHeartbeat: (value) => `마지막 하트비트 ${value}`,
      capabilities: (count) => `기능 ${count}개`,
    },
    conditions: { available: "사용 가능", offline: "오프라인", maintenance: "유지 관리" },
  },
};

export function NodesPage() {
  const copy = useCopy(nodesCopy);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = () => {
    setLoading(true);
    setError("");
    api.nodes()
      .then(setNodes)
      .catch((reason) => setError(reason instanceof Error ? reason.message : copy.page.loadError))
      .finally(() => setLoading(false));
  };

  useEffect(() => { load(); }, []);

  if (loading && !nodes.length) return <section className="page"><LoadingState label={copy.page.loading} /></section>;
  if (error && !nodes.length) return <section className="page"><ErrorState message={error} onRetry={load} /></section>;

  const reservedMemory = nodes.reduce((total, node) => total + node.allocatedMemoryBytes, 0);
  const totalMemory = nodes.reduce((total, node) => total + node.memoryBytes, 0);
  const runningServers = nodes.reduce((total, node) => total + node.runningServers, 0);

  return (
    <section className="page nodes-page">
      <div className="page-heading page-heading-wide">
        <div><h1>{copy.page.title}</h1><p className="lede">{copy.page.description}</p></div>
        <div className="heading-actions"><span className="sync-state"><i />{loading ? copy.page.syncing : copy.page.snapshot}</span><button className="button secondary" onClick={load}><RefreshCw size={16} />{copy.page.refresh}</button></div>
      </div>
      <div className="node-kpi-row">
        <Kpi label={copy.kpi.available} value={`${nodes.filter((node) => node.condition === "available").length}`} detail={copy.kpi.registered(nodes.length)} tone="mint" />
        <Kpi label={copy.kpi.reservedMemory} value={formatBytes(reservedMemory)} detail={copy.kpi.capacity(formatBytes(totalMemory))} tone="blue" />
        <Kpi label={copy.kpi.reportedWorkloads} value={`${runningServers}`} detail={copy.kpi.latestSnapshot} tone="mint" />
      </div>
      <div className="node-card-grid">{nodes.map((node) => <NodeCard key={node.id} node={node} copy={copy} />)}</div>
      <div className="panel node-registry-panel">
        <div className="panel-heading"><div><p className="eyebrow">{copy.page.registry}</p><h2>{copy.page.registeredNodes}</h2></div></div>
        <div className="server-table-wrap"><table className="data-table node-table"><thead><tr><th>{copy.table.node}</th><th>{copy.table.condition}</th><th>{copy.table.agent}</th><th>{copy.table.heartbeat}</th><th>{copy.table.servers}</th><th>{copy.table.capacity}</th></tr></thead><tbody>
          {nodes.map((node) => <tr key={node.id}><td><div className="node-cell"><span className={`node-health-dot node-${node.condition}`} /><span translate="no"><strong>{node.name}</strong><small>{node.region} · {node.address}</small></span></div></td><td><StatusBadge tone={toneForNode(node.condition)}>{copy.conditions[node.condition]}</StatusBadge></td><td><code translate="no">{node.version}</code></td><td><span className="muted-text">{relativeTime(node.lastHeartbeatAt)}</span><small className="table-subline">{formatDateTime(node.lastHeartbeatAt)}</small></td><td><strong>{node.runningServers}</strong><span className="muted-text"> / {node.totalServers}</span></td><td><div className="capacity-inline"><div className="mini-progress"><i style={{ width: `${Math.round((node.allocatedMemoryBytes / node.memoryBytes) * 100)}%` }} /></div><span>{Math.round((node.allocatedMemoryBytes / node.memoryBytes) * 100)}%</span></div></td></tr>)}
        </tbody></table></div>
      </div>
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
