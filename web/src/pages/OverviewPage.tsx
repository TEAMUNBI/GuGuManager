import { useEffect, useState } from "react";
import { Activity, ArrowUpRight, ChevronRight, CircleAlert, Gauge, HardDrive, MemoryStick, Plus, Server, Wifi } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import type { AuditEvent, Node, Overview, Server as ServerModel } from "../lib/types";
import { actionLabel, formatBytes, nodeLabel, powerLabel, relativeTime, snapshotLabel } from "../lib/format";
import { ErrorState, LoadingState } from "../components/PageState";
import { StatusBadge, toneForNode, toneForPower } from "../components/StatusBadge";
import { type LocalizedCopy, useCopy } from "../i18n/I18n";

interface OverviewCopy {
  loadError: string;
  loading: string;
  eyebrow: string;
  title: string;
  description: string;
  newServer: string;
  fleetSteady: string;
  runningAcross: (running: number, nodes: number) => string;
  servers: string;
  serversTitle: string;
  aggregateCpu: string;
  runningServers: string;
  provisioned: (count: number) => string;
  nodeAvailability: string;
  availableSnapshots: string;
  memoryReserved: string;
  capacity: (value: string) => string;
  openOperations: string;
  awaitingReconciliation: string;
  activeFleet: string;
  viewAll: string;
  server: string;
  state: string;
  allocation: string;
  load: string;
  nodePressure: string;
  capacityTitle: string;
  viewNodes: string;
  memory: string;
  disk: string;
  reservedDisk: string;
  recentActivity: string;
  auditStream: string;
  openAudit: string;
  emptyServersTitle: string;
  emptyServersDetail: string;
  emptyNodesTitle: string;
  emptyNodesDetail: string;
  emptyActivityTitle: string;
  emptyActivityDetail: string;
  openServer: (name: string) => string;
  controlPlane: string;
  refresh: string;
  result: Record<AuditEvent["result"], string>;
  ok: string;
  ack: string;
  fail: string;
}

const overviewCopy: LocalizedCopy<OverviewCopy> = {
  "zh-CN": {
    loadError: "无法加载运行概览", loading: "正在读取运行状态", eyebrow: "运行概览 / ", title: "服务器总览", description: "查看服务器、节点资源与后台任务的实时状态。", newServer: "新建服务器", fleetSteady: "整体运行平稳", runningAcross: (running, nodes) => `${running} 台服务器正在运行，当前 ${nodes} 个节点在线。`, servers: "服务器", serversTitle: "服务器", aggregateCpu: "CPU 使用率", runningServers: "运行中", provisioned: (count) => `共 ${count} 台服务器`, nodeAvailability: "在线节点", availableSnapshots: `节点在线情况`, memoryReserved: "已分配内存", capacity: (value) => `总容量 ${value}`, openOperations: "进行中任务", awaitingReconciliation: "等待状态同步", activeFleet: "服务器状态", viewAll: "查看全部", server: "服务器", state: "运行状态", allocation: "连接地址", load: "CPU", nodePressure: "节点资源", capacityTitle: "资源占用", viewNodes: "查看所有节点", memory: "内存", disk: "磁盘", reservedDisk: "已分配磁盘", recentActivity: "近期记录", auditStream: "最近操作", openAudit: "查看操作日志", openServer: (name) => `打开服务器 ${name}`, controlPlane: "管理服务", refresh: "刷新", result: { accepted: "已受理", success: "成功", failure: "失败" }, ok: "成功", ack: "已受理", fail: "失败",
    emptyServersTitle: "还没有服务器", emptyServersDetail: "新建服务器后，运行状态与资源负载会显示在这里。", emptyNodesTitle: "还没有可用节点", emptyNodesDetail: "连接节点 Agent 后，这里会显示节点容量与健康状态。", emptyActivityTitle: "还没有操作记录", emptyActivityDetail: "服务器和管理服务的操作会按时间显示在这里。",
  },
  en: {
    loadError: "Unable to load overview.", loading: "Reading server state", eyebrow: "OPERATIONS / ", title: "Server overview", description: "Live server health, node capacity, and background work in one place.", newServer: "New server", fleetSteady: "Everything is running steadily", runningAcross: (running, nodes) => `${running} ${running === 1 ? "server is" : "servers are"} running; ${nodes} ${nodes === 1 ? "node is" : "nodes are"} online.`, servers: "servers", serversTitle: "Servers", aggregateCpu: "CPU usage", runningServers: "Running", provisioned: (count) => `${count} ${count === 1 ? "server" : "servers"} total`, nodeAvailability: "Online nodes", availableSnapshots: "current node availability", memoryReserved: "Allocated memory", capacity: (value) => `${value} total`, openOperations: "Active tasks", awaitingReconciliation: "awaiting state sync", activeFleet: "SERVER STATUS", viewAll: "View all", server: "Server", state: "Status", allocation: "Address", load: "CPU", nodePressure: "NODE RESOURCES", capacityTitle: "Utilization", viewNodes: "View all nodes", memory: "Memory", disk: "Disk", reservedDisk: "Allocated disk", recentActivity: "RECENT LOG", auditStream: "Latest activity", openAudit: "View activity log", openServer: (name) => `Open server ${name}`, controlPlane: "Control plane", refresh: "Refresh", result: { accepted: "accepted", success: "success", failure: "failure" }, ok: "OK", ack: "ACCEPTED", fail: "FAILED",
    emptyServersTitle: "No servers yet", emptyServersDetail: "Create a server to see its runtime status and resource load here.", emptyNodesTitle: "No nodes connected", emptyNodesDetail: "Connect a node agent to see capacity and health here.", emptyActivityTitle: "No activity yet", emptyActivityDetail: "Server and control plane activity will appear here in chronological order.",
  },
  ja: {
    loadError: "概要を読み込めません。", loading: "サーバー状態を読み込み中", eyebrow: "運用概要 / ", title: "サーバー概要", description: "サーバー、ノードの容量、バックグラウンド処理をまとめて確認できます。", newServer: "サーバーを作成", fleetSteady: "全体は安定して稼働しています", runningAcross: (running, nodes) => `${running} 台のサーバーが稼働中で、${nodes} 台のノードがオンラインです。`, servers: "サーバー", serversTitle: "サーバー", aggregateCpu: "CPU 使用率", runningServers: "稼働中", provisioned: (count) => `全 ${count} 台`, nodeAvailability: "オンラインノード", availableSnapshots: "現在のノード状態", memoryReserved: "割り当て済みメモリ", capacity: (value) => `合計 ${value}`, openOperations: "実行中のタスク", awaitingReconciliation: "状態同期を待機中", activeFleet: "サーバー状態", viewAll: "すべて表示", server: "サーバー", state: "状態", allocation: "接続先", load: "CPU", nodePressure: "ノードリソース", capacityTitle: "使用状況", viewNodes: "すべてのノードを表示", memory: "メモリ", disk: "ディスク", reservedDisk: "割り当て済みディスク", recentActivity: "最近の記録", auditStream: "最近の操作", openAudit: "操作ログを表示", openServer: (name) => `サーバー ${name} を開く`, controlPlane: "管理サービス", refresh: "更新", result: { accepted: "受付済み", success: "成功", failure: "失敗" }, ok: "成功", ack: "受付済み", fail: "失敗",
    emptyServersTitle: "サーバーはまだありません", emptyServersDetail: "サーバーを作成すると、稼働状態とリソース使用量がここに表示されます。", emptyNodesTitle: "接続済みのノードはありません", emptyNodesDetail: "ノードエージェントを接続すると、容量と健全性がここに表示されます。", emptyActivityTitle: "操作履歴はまだありません", emptyActivityDetail: "サーバーと管理サービスの操作が時系列でここに表示されます。",
  },
  ko: {
    loadError: "개요를 불러올 수 없습니다.", loading: "서버 상태를 읽는 중", eyebrow: "운영 개요 / ", title: "서버 개요", description: "서버 상태, 노드 용량, 백그라운드 작업을 한곳에서 확인합니다.", newServer: "서버 만들기", fleetSteady: "전체 시스템이 안정적으로 실행 중입니다", runningAcross: (running, nodes) => `서버 ${running}개가 실행 중이며 노드 ${nodes}개가 온라인입니다.`, servers: "서버", serversTitle: "서버", aggregateCpu: "CPU 사용률", runningServers: "실행 중", provisioned: (count) => `전체 ${count}개`, nodeAvailability: "온라인 노드", availableSnapshots: "현재 노드 상태", memoryReserved: "할당된 메모리", capacity: (value) => `전체 ${value}`, openOperations: "진행 중인 작업", awaitingReconciliation: "상태 동기화 대기 중", activeFleet: "서버 상태", viewAll: "모두 보기", server: "서버", state: "상태", allocation: "접속 주소", load: "CPU", nodePressure: "노드 리소스", capacityTitle: "사용량", viewNodes: "모든 노드 보기", memory: "메모리", disk: "디스크", reservedDisk: "할당된 디스크", recentActivity: "최근 기록", auditStream: "최근 작업", openAudit: "작업 로그 보기", openServer: (name) => `서버 ${name} 열기`, controlPlane: "관리 서비스", refresh: "새로 고침", result: { accepted: "접수됨", success: "성공", failure: "실패" }, ok: "성공", ack: "접수됨", fail: "실패",
    emptyServersTitle: "아직 서버가 없습니다", emptyServersDetail: "서버를 만들면 실행 상태와 리소스 사용량이 여기에 표시됩니다.", emptyNodesTitle: "연결된 노드가 없습니다", emptyNodesDetail: "노드 에이전트를 연결하면 용량과 상태가 여기에 표시됩니다.", emptyActivityTitle: "아직 작업 기록이 없습니다", emptyActivityDetail: "서버와 관리 서비스의 작업 내역이 시간순으로 여기에 표시됩니다.",
  },
};

const overviewHeadingCopy: LocalizedCopy<{
  eyebrow: string;
  title: string;
  description: string;
  fleetAttention: string;
  openAudit: string;
}> = {
  "zh-CN": {
    eyebrow: "运维 / ",
    title: "运行总览",
    description: "查看服务器、节点资源与任务队列的实时状态。",
    fleetAttention: "节点连接需要检查",
    openAudit: "查看审计日志",
  },
  en: {
    eyebrow: "OPERATIONS / ",
    title: "Operations overview",
    description: "Monitor server health, node capacity, and the task queue in one place.",
    fleetAttention: "Node connectivity needs attention",
    openAudit: "View audit log",
  },
  ja: {
    eyebrow: "運用 / ",
    title: "運用概要",
    description: "サーバーの状態、ノード容量、タスクキューをまとめて確認できます。",
    fleetAttention: "ノード接続を確認してください",
    openAudit: "監査ログを表示",
  },
  ko: {
    eyebrow: "운영 / ",
    title: "운영 개요",
    description: "서버 상태, 노드 용량, 작업 대기열을 한곳에서 확인합니다.",
    fleetAttention: "노드 연결을 확인해야 합니다",
    openAudit: "감사 로그 보기",
  },
};

export function OverviewPage() {
  const copy = useCopy(overviewCopy);
  const headingCopy = useCopy(overviewHeadingCopy);
  const navigate = useNavigate();
  const [overview, setOverview] = useState<Overview | null>(null);
  const [servers, setServers] = useState<ServerModel[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [activity, setActivity] = useState<AuditEvent[]>([]);
  const [error, setError] = useState("");
  const [lastLoadedAt, setLastLoadedAt] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const load = () => {
    setError("");
    Promise.all([api.overview(), api.servers(), api.nodes(), api.audit()]).then(([nextOverview, nextServers, nextNodes, nextActivity]) => { setOverview(nextOverview); setServers(nextServers); setNodes(nextNodes); setActivity(nextActivity); setLastLoadedAt(new Date().toISOString()); setStale(false); }).catch((reason) => { setError(reason instanceof Error ? reason.message : copy.loadError); setStale(true); });
  };
  useEffect(() => { load(); const timer = window.setInterval(load, 5000); return () => window.clearInterval(timer); }, [copy.loadError]);
  if (error && !overview) return <section className="page"><ErrorState message={error} onRetry={load} /></section>;
  if (!overview) return <section className="page"><LoadingState label={copy.loading} /></section>;
  const fleetNeedsAttention = overview.totalNodeCount > 0 && overview.onlineNodeCount < overview.totalNodeCount;
  return <section className="page overview-page">
    <div className="page-heading page-heading-wide"><div><h1>{headingCopy.title}</h1><p className="lede">{headingCopy.description}</p></div><div className="heading-actions"><span className={`refresh-note${stale ? " is-stale" : ""}`}><i />{snapshotLabel(lastLoadedAt, stale)}</span><button className="button primary" onClick={() => navigate("/servers?create=1")}><Plus size={17} />{copy.newServer}</button></div></div>
    <div className={`fleet-banner${fleetNeedsAttention ? " is-attention" : ""}`}><div className="fleet-banner-mark"><Gauge size={23} /></div><div><strong>{fleetNeedsAttention ? headingCopy.fleetAttention : copy.fleetSteady}</strong><span>{copy.runningAcross(overview.runningServerCount, overview.onlineNodeCount)}</span></div></div>
    <div className="metric-grid"><MetricCard icon={<Server size={17} />} label={copy.runningServers} value={`${overview.runningServerCount}`} detail={copy.provisioned(overview.serverCount)} tone="mint" /><MetricCard icon={<Wifi size={17} />} label={copy.nodeAvailability} value={`${overview.onlineNodeCount}/${overview.totalNodeCount}`} detail={copy.availableSnapshots} tone="blue" /><MetricCard icon={<MemoryStick size={17} />} label={copy.memoryReserved} value={formatBytes(overview.memoryUsedBytes)} detail={copy.capacity(formatBytes(overview.memoryTotalBytes))} tone="blue" /><MetricCard icon={<CircleAlert size={17} />} label={copy.openOperations} value={`${overview.queuedOperationCount}`} detail={copy.awaitingReconciliation} tone={overview.queuedOperationCount ? "orange" : "mint"} /></div>
    <div className="split-grid overview-main-grid">
      <div className="panel fleet-panel">
        <div className="panel-heading"><div><p className="eyebrow">{copy.activeFleet}</p><h2>{copy.serversTitle}</h2></div><Link className="text-link" to="/servers">{copy.viewAll} <ChevronRight size={15} /></Link></div>
        {servers.length > 0 ? <div className="server-table-wrap"><table className="data-table"><thead><tr><th>{copy.server}</th><th>{copy.state}</th><th>{copy.allocation}</th><th>{copy.load}</th><th /></tr></thead><tbody>{servers.slice(0, 4).map((server) => <ServerRow key={server.id} server={server} />)}</tbody></table></div> : <OverviewEmptyState icon={<Server size={25} />} title={copy.emptyServersTitle} detail={copy.emptyServersDetail} />}
      </div>
      <div className="panel node-panel">
        <div className="panel-heading"><div><p className="eyebrow">{copy.nodePressure}</p><h2>{copy.capacityTitle}</h2></div><Link className="icon-link" to="/nodes" aria-label={copy.viewNodes}><ArrowUpRight size={17} /></Link></div>
        {nodes.length > 0 ? <><div className="node-list">{nodes.map((node) => <NodeLoad key={node.id} node={node} copy={copy} />)}</div><div className="capacity-foot"><HardDrive size={15} /><span>{copy.reservedDisk}</span><b>{formatBytes(nodes.reduce((total, node) => total + node.allocatedDiskBytes, 0))}</b></div></> : <OverviewEmptyState icon={<Wifi size={25} />} title={copy.emptyNodesTitle} detail={copy.emptyNodesDetail} />}
      </div>
    </div>
    <div className="panel activity-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.recentActivity}</p><h2>{copy.auditStream}</h2></div><Link className="text-link" to="/audit">{headingCopy.openAudit} <ChevronRight size={15} /></Link></div>{activity.length > 0 ? <ActivityList items={activity.slice(0, 5)} copy={copy} /> : <OverviewEmptyState icon={<Activity size={25} />} title={copy.emptyActivityTitle} detail={copy.emptyActivityDetail} />}</div>
    {error && <div className="inline-warning"><CircleAlert size={15} />{error}<button onClick={load}>{copy.refresh}</button></div>}
  </section>;
}

function MetricCard({ icon, label, value, detail, tone }: { icon: React.ReactNode; label: string; value: string; detail: string; tone: "mint" | "orange" | "blue" }) {
  return <div className={`metric-card metric-${tone}`}><div className="metric-card-top"><span className="metric-icon">{icon}</span><span>{label}</span></div><strong>{value}</strong><small>{detail}</small></div>;
}

function OverviewEmptyState({ icon, title, detail }: { icon: React.ReactNode; title: string; detail: string }) {
  return <div className="empty-state overview-empty-state">{icon}<strong>{title}</strong><span>{detail}</span></div>;
}

function ServerRow({ server }: { server: ServerModel }) {
  const copy = useCopy(overviewCopy);
  return <tr><td><Link className="table-primary" to={`/servers/${server.id}`}><span className={`game-glyph game-${server.gameName.toLowerCase().replace(/[^a-z]/g, "")}`}>{server.gameName.slice(0, 1)}</span><span translate="no"><strong>{server.name}</strong><small>{server.gameName} / {server.nodeName}</small></span></Link></td><td><StatusBadge tone={toneForPower(server.observedPower)} pulse={server.observedPower === "starting" || server.observedPower === "stopping"}>{powerLabel[server.observedPower]}</StatusBadge></td><td><code translate="no">{server.allocation}</code></td><td><div className="table-load"><span>{Math.round(server.metrics.cpuPercent)}%</span><div className="mini-progress"><i style={{ width: `${Math.min(100, server.metrics.cpuPercent)}%` }} /></div></div></td><td><Link className="row-arrow" to={`/servers/${server.id}`} aria-label={copy.openServer(server.name)}><ChevronRight size={16} /></Link></td></tr>;
}

function NodeLoad({ node, copy }: { node: Node; copy: OverviewCopy }) {
  const memory = node.memoryBytes ? (node.allocatedMemoryBytes / node.memoryBytes) * 100 : 0;
  const disk = node.diskBytes ? (node.allocatedDiskBytes / node.diskBytes) * 100 : 0;
  return <div className="node-load"><div className="node-load-head"><span className={`node-health-dot node-${node.condition}`} /><span className="node-name" translate="no">{node.name}</span><StatusBadge tone={toneForNode(node.condition)}>{nodeLabel[node.condition]}</StatusBadge></div><div className="node-load-line"><span>{copy.memory}</span><div className="mini-progress"><i style={{ width: `${Math.min(100, memory)}%` }} /></div><b>{Math.round(memory)}%</b></div><div className="node-load-line"><span>{copy.disk}</span><div className="mini-progress progress-blue"><i style={{ width: `${Math.min(100, disk)}%` }} /></div><b>{Math.round(disk)}%</b></div></div>;
}

export function ActivityList({ items, copy: providedCopy }: { items: AuditEvent[]; copy?: OverviewCopy }) {
  const copy = useCopy(overviewCopy);
  const labels = providedCopy ?? copy;
  return <div className="activity-list">{items.map((event) => <div className="activity-row" key={event.id}><span className={`activity-mark ${event.result}`}><span /></span><div className="activity-copy"><strong>{actionLabel[event.action] ?? event.action}</strong><span>{event.targetName === "Control Plane" ? labels.controlPlane : event.targetName} · {event.actorName}</span></div><time dateTime={event.createdAt}>{relativeTime(event.createdAt)}</time><span className={`activity-result ${event.result}`}>{event.result === "success" ? labels.ok : event.result === "accepted" ? labels.ack : labels.fail}</span></div>)}</div>;
}
