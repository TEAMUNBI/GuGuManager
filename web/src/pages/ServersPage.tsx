import { useEffect, useMemo, useState } from "react";
import { ChevronRight, CircleAlert, Filter, Network, Plus, Search, Server } from "lucide-react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import type { GameDefinition, Node, Operation, Server as ServerModel } from "../lib/types";
import { operationFailureMessage, pollOperation } from "../lib/operations";
import { formatBytes, nodeLabel, powerLabel, relativeTime } from "../lib/format";
import { Modal } from "../components/Modal";
import { ErrorState, LoadingState } from "../components/PageState";
import { StatusBadge, toneForPower } from "../components/StatusBadge";
import { useAppContext } from "../app/App";
import { type LocalizedCopy, useCopy, useI18n } from "../i18n/I18n";
import { localizedGameSummary } from "../lib/gamePresentation";

type FilterValue = "all" | "running" | "stopped" | "attention";

interface ServersCopy {
  loadError: string;
  loading: string;
  eyebrow: string;
  title: string;
  description: string;
  syncing: string;
  snapshot: string;
  newServer: string;
  provisionedServers: string;
  assignedServers: string;
  runningNow: string;
  needAttention: string;
  filterAria: string;
  filter: Record<FilterValue, string>;
  search: string;
  searchAria: string;
  visible: (count: number) => string;
  scope: string;
  noMatch: string;
  adminEmpty: string;
  memberEmpty: string;
  cpu: string;
  memory: string;
  updated: string;
  createTitle: string;
  createDescription: string;
  cancel: string;
  provisioning: string;
  create: string;
  gameRelease: string;
  definition: string;
  serverName: string;
  namePlaceholder: string;
  node: string;
  memoryMb: string;
  diskGb: string;
  gameMissing: string;
  noRunnableGames: string;
  gameUnavailable: string;
  createFailed: string;
  accepted: string;
  completed: string;
  statusUnavailable: string;
  retry: string;
  operationStatus: Record<Operation["status"], string>;
  terminalFallback: (status: string) => string;
}

const serversCopy: LocalizedCopy<ServersCopy> = {
  "zh-CN": {
    loadError: "无法加载服务器列表", loading: "正在读取服务器列表", eyebrow: "运行管理 / 服务器", title: "服务器", description: "集中查看每台游戏服务器的运行状态、资源用量和连接信息。", syncing: "同步中", snapshot: "状态快照", newServer: "新建服务器", provisionedServers: "服务器总数", assignedServers: "已授权服务器", runningNow: "正在运行", needAttention: "需要处理", filterAria: "按状态筛选服务器", filter: { all: "全部", running: "运行中", stopped: "已停止", attention: "需处理" }, search: "搜索名称、游戏或节点", searchAria: "搜索服务器", visible: (count) => `显示 ${count} 台`, scope: "仅显示已授权的服务器", noMatch: "没有符合当前条件的服务器。", adminEmpty: "可以调整筛选条件，或新建一台服务器。", memberEmpty: "你当前没有可访问的服务器。", cpu: "CPU", memory: "内存", updated: "最近更新", createTitle: "新建服务器", createDescription: "选择已批准且可运行的游戏模板和运行节点，并设置初始资源配额。", cancel: "取消", provisioning: "正在创建…", create: "创建服务器", gameRelease: "游戏模板", definition: "模板版本", serverName: "服务器名称", namePlaceholder: "例如：月光谷", node: "运行节点", memoryMb: "内存（MB）", diskGb: "磁盘（GB）", gameMissing: "所选游戏模板不存在", noRunnableGames: "当前没有已批准且可运行的游戏模板，因此不能创建服务器。", gameUnavailable: "所选游戏模板未获批准或不可运行", createFailed: "服务器创建请求失败", accepted: "创建任务已受理", completed: "服务器创建完成", statusUnavailable: "暂时无法获取创建进度", retry: "重试", operationStatus: { queued: "等待中", leased: "已领取", running: "执行中", succeeded: "已完成", failed: "失败" }, terminalFallback: (status) => `创建任务状态：${status}`,
  },
  en: {
    loadError: "Unable to load servers.", loading: "Loading servers", eyebrow: "OPERATIONS / SERVERS", title: "Servers", description: "Monitor each game server's runtime status, resource usage, and connection details.", syncing: "Syncing", snapshot: "Status snapshot", newServer: "New server", provisionedServers: "servers total", assignedServers: "authorized servers", runningNow: "running", needAttention: "need attention", filterAria: "Filter servers by status", filter: { all: "All", running: "Running", stopped: "Stopped", attention: "Needs attention" }, search: "Search name, game, or node", searchAria: "Search servers", visible: (count) => `${count} visible`, scope: "Authorized servers only", noMatch: "No servers match the current filters.", adminEmpty: "Change the filters or create a server.", memberEmpty: "You do not have access to any servers.", cpu: "CPU", memory: "Memory", updated: "Last updated", createTitle: "Create server", createDescription: "Select an approved, runnable game template and node, then set the initial resource allocation.", cancel: "Cancel", provisioning: "Creating...", create: "Create server", gameRelease: "Game template", definition: "Template version", serverName: "Server name", namePlaceholder: "e.g. Moonlit Valley", node: "Node", memoryMb: "Memory (MB)", diskGb: "Disk (GB)", gameMissing: "The selected game template is unavailable", noRunnableGames: "No game template is currently approved and runnable, so server creation is disabled.", gameUnavailable: "The selected game template is not approved or runnable", createFailed: "Unable to request server creation", accepted: "Server creation accepted", completed: "Server created", statusUnavailable: "Server creation status is unavailable", retry: "Retry", operationStatus: { queued: "Queued", leased: "Claimed", running: "Running", succeeded: "Succeeded", failed: "Failed" }, terminalFallback: (status) => `Server creation status: ${status}`,
  },
  ja: {
    loadError: "サーバーを読み込めません。", loading: "サーバー一覧を読み込み中", eyebrow: "運用 / サーバー", title: "サーバー", description: "各ゲームサーバーの稼働状態、リソース使用量、接続情報をまとめて確認します。", syncing: "同期中", snapshot: "状態スナップショット", newServer: "サーバーを作成", provisionedServers: "サーバー総数", assignedServers: "アクセス可能なサーバー", runningNow: "稼働中", needAttention: "要確認", filterAria: "状態でサーバーを絞り込む", filter: { all: "すべて", running: "稼働中", stopped: "停止済み", attention: "要確認" }, search: "名前、ゲーム、ノードを検索", searchAria: "サーバーを検索", visible: (count) => `${count} 台を表示`, scope: "アクセス可能なサーバーのみ表示", noMatch: "現在の条件に一致するサーバーはありません。", adminEmpty: "条件を変更するか、新しいサーバーを作成してください。", memberEmpty: "アクセスできるサーバーはありません。", cpu: "CPU", memory: "メモリ", updated: "最終更新", createTitle: "サーバーを作成", createDescription: "承認済みで実行可能なゲームテンプレートと稼働ノードを選択し、初期リソース割り当てを設定します。", cancel: "キャンセル", provisioning: "作成中…", create: "サーバーを作成", gameRelease: "ゲームテンプレート", definition: "テンプレートバージョン", serverName: "サーバー名", namePlaceholder: "例：月明かりの谷", node: "稼働ノード", memoryMb: "メモリ（MB）", diskGb: "ディスク（GB）", gameMissing: "選択したゲームテンプレートを利用できません", noRunnableGames: "承認済みで実行可能なゲームテンプレートがないため、サーバーを作成できません。", gameUnavailable: "選択したゲームテンプレートは未承認または実行不可です", createFailed: "サーバー作成を要求できません", accepted: "サーバー作成を受け付けました", completed: "サーバーを作成しました", statusUnavailable: "サーバー作成の進行状況を取得できません", retry: "再試行", operationStatus: { queued: "待機中", leased: "取得済み", running: "実行中", succeeded: "完了", failed: "失敗" }, terminalFallback: (status) => `サーバー作成の状態：${status}`,
  },
  ko: {
    loadError: "서버를 불러올 수 없습니다.", loading: "서버 목록을 불러오는 중", eyebrow: "운영 / 서버", title: "서버", description: "각 게임 서버의 실행 상태, 리소스 사용량, 연결 정보를 한곳에서 확인합니다.", syncing: "동기화 중", snapshot: "상태 스냅샷", newServer: "서버 만들기", provisionedServers: "전체 서버", assignedServers: "접근 가능한 서버", runningNow: "실행 중", needAttention: "확인 필요", filterAria: "상태별 서버 필터", filter: { all: "전체", running: "실행 중", stopped: "중지됨", attention: "확인 필요" }, search: "이름, 게임 또는 노드 검색", searchAria: "서버 검색", visible: (count) => `${count}개 표시`, scope: "접근 가능한 서버만 표시", noMatch: "현재 조건과 일치하는 서버가 없습니다.", adminEmpty: "조건을 변경하거나 새 서버를 만드세요.", memberEmpty: "접근할 수 있는 서버가 없습니다.", cpu: "CPU", memory: "메모리", updated: "마지막 업데이트", createTitle: "서버 만들기", createDescription: "승인되고 실행 가능한 게임 템플릿과 실행 노드를 선택하고 초기 리소스 할당량을 설정합니다.", cancel: "취소", provisioning: "생성 중…", create: "서버 만들기", gameRelease: "게임 템플릿", definition: "템플릿 버전", serverName: "서버 이름", namePlaceholder: "예: 달빛 골짜기", node: "실행 노드", memoryMb: "메모리 (MB)", diskGb: "디스크 (GB)", gameMissing: "선택한 게임 템플릿을 사용할 수 없습니다", noRunnableGames: "승인되고 실행 가능한 게임 템플릿이 없어 서버 생성을 사용할 수 없습니다.", gameUnavailable: "선택한 게임 템플릿이 승인되지 않았거나 실행할 수 없습니다", createFailed: "서버 생성을 요청할 수 없습니다", accepted: "서버 생성 요청이 접수되었습니다", completed: "서버를 생성했습니다", statusUnavailable: "서버 생성 진행 상태를 확인할 수 없습니다", retry: "다시 시도", operationStatus: { queued: "대기 중", leased: "할당됨", running: "실행 중", succeeded: "완료", failed: "실패" }, terminalFallback: (status) => `서버 생성 상태: ${status}`,
  },
};

function serverNeedsAttention(server: ServerModel): boolean {
  return server.nodeCondition !== "available"
    || server.healthCondition === "unhealthy"
    || server.observedPower === "unknown";
}

export function ServersPage() {
  const copy = useCopy(serversCopy);
  const { session, toast } = useAppContext();
  const isAdmin = session.user.roles.includes("platform_admin");
  const location = useLocation();
  const navigate = useNavigate();
  const [servers, setServers] = useState<ServerModel[]>([]);
  const [games, setGames] = useState<GameDefinition[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [filter, setFilter] = useState<FilterValue>("all");
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(isAdmin && new URLSearchParams(location.search).get("create") === "1");
  const load = () => {
    setLoading(true);
    const request = isAdmin ? Promise.all([api.servers(), api.games(), api.nodes()]).then(([nextServers, nextGames, nextNodes]) => { setServers(nextServers); setGames(nextGames); setNodes(nextNodes); }) : Promise.all([api.servers(), api.games()]).then(([nextServers, nextGames]) => { setServers(nextServers); setGames(nextGames); setNodes([]); });
    request.then(() => setError("")).catch((reason) => setError(reason instanceof Error ? reason.message : copy.loadError)).finally(() => setLoading(false));
  };
  useEffect(() => { load(); }, [copy.loadError]);
  const visible = useMemo(() => servers.filter((server) => {
    const matchesQuery = `${server.name} ${server.gameName} ${server.nodeName}`.toLowerCase().includes(query.toLowerCase());
    const matchesFilter = filter === "all" || (filter === "running" && server.observedPower === "running") || (filter === "stopped" && server.observedPower === "stopped") || (filter === "attention" && serverNeedsAttention(server));
    return matchesQuery && matchesFilter;
  }), [filter, query, servers]);
  const attentionCount = servers.filter(serverNeedsAttention).length;
  const onCreated = async (operation: Operation): Promise<void> => {
    setCreateOpen(false);
    toast(copy.accepted, "warning");
    try {
      const completed = await pollOperation(operation, api.operation);
      load();
      if (completed.status === "succeeded") { toast(copy.completed, "success"); navigate(`/servers/${operation.serverId}`); }
      else toast(operationFailureMessage(completed, copy.terminalFallback(copy.operationStatus[completed.status])), "danger");
    } catch (reason) { load(); toast(reason instanceof Error ? reason.message : copy.statusUnavailable, "danger"); }
  };
  if (loading && !servers.length) return <section className="page"><LoadingState label={copy.loading} /></section>;
  if (error && !servers.length) return <section className="page"><ErrorState message={error} onRetry={load} /></section>;
  return <section className="page servers-page"><div className="page-heading page-heading-wide"><div><h1>{copy.title}</h1><p className="lede">{copy.description}</p></div><div className="heading-actions"><span className="sync-state"><i />{loading ? copy.syncing : copy.snapshot}</span>{isAdmin && <button className="button primary" onClick={() => setCreateOpen(true)}><Plus size={17} />{copy.newServer}</button>}</div></div><div className="server-summary-row"><div className="summary-stamp"><Server size={17} /><strong>{servers.length}</strong><span>{isAdmin ? copy.provisionedServers : copy.assignedServers}</span></div><div className="summary-stamp"><span className="stamp-dot mint" /><strong>{servers.filter((server) => server.observedPower === "running").length}</strong><span>{copy.runningNow}</span></div><div className="summary-stamp"><span className="stamp-dot orange" /><strong>{attentionCount}</strong><span>{copy.needAttention}</span></div></div><div className="toolbar-row"><div className="segmented-control" role="tablist" aria-label={copy.filterAria}>{(["all", "running", "stopped", "attention"] as FilterValue[]).map((value) => <button key={value} className={filter === value ? "active" : ""} onClick={() => setFilter(value)} role="tab" aria-selected={filter === value}>{copy.filter[value]}<span>{value === "all" ? servers.length : value === "running" ? servers.filter((server) => server.observedPower === "running").length : value === "stopped" ? servers.filter((server) => server.observedPower === "stopped").length : attentionCount}</span></button>)}</div><div className="toolbar-tools"><label className="search-input"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={copy.search} aria-label={copy.searchAria} /></label></div></div><div className="panel server-list-panel"><div className="list-panel-head"><span>{copy.visible(visible.length)}</span><span className="list-head-note"><Filter size={14} />{copy.scope}</span></div><div className="server-grid">{visible.map((server) => <ServerListItem key={server.id} server={server} copy={copy} />)}{!visible.length && <div className="empty-state"><Server size={25} /><strong>{copy.noMatch}</strong><span>{isAdmin ? copy.adminEmpty : copy.memberEmpty}</span>{isAdmin && <button className="button secondary" onClick={() => setCreateOpen(true)}><Plus size={16} />{copy.newServer}</button>}</div>}</div></div>{error && <div className="inline-warning"><CircleAlert size={15} />{error}</div>}{isAdmin && <CreateServerModal open={createOpen} games={games} nodes={nodes} csrfToken={session.csrfToken} onClose={() => setCreateOpen(false)} onCreated={onCreated} copy={copy} />}</section>;
}

function ServerListItem({ server, copy }: { server: ServerModel; copy: ServersCopy }) {
  return <Link className="server-list-item" to={`/servers/${server.id}`}><div className={`game-avatar game-${server.gameName.toLowerCase().replace(/[^a-z]/g, "")}`}>{server.gameName.slice(0, 1)}</div><div className="server-list-main"><div className="server-title-row"><strong translate="no">{server.name}</strong><StatusBadge tone={toneForPower(server.observedPower)} pulse={server.observedPower === "starting" || server.observedPower === "stopping"}>{powerLabel[server.observedPower]}</StatusBadge>{server.healthCondition === "unhealthy" && <StatusBadge tone="danger">{powerLabel.unhealthy}</StatusBadge>}</div><span translate="no">{server.gameName} {server.gameVersion} <i /> {server.ownerName}</span><div className="server-runtime-meta"><span><span className={`node-health-dot node-${server.nodeCondition}`} aria-hidden="true" /><span translate="no">{server.nodeName}</span><small className={`node-condition-text node-condition-${server.nodeCondition}`}>{nodeLabel[server.nodeCondition]}</small></span><code translate="no"><Network size={13} />{server.allocation}</code></div></div><div className="server-list-metrics"><div><span>{copy.cpu}</span><strong>{Math.round(server.metrics.cpuPercent)}%</strong></div><div><span>{copy.memory}</span><strong>{formatBytes(server.metrics.memoryBytes)}</strong></div><div className="server-list-time"><span>{copy.updated}</span><strong>{relativeTime(server.updatedAt)}</strong></div></div><ChevronRight className="list-chevron" size={18} /></Link>;
}

function CreateServerModal({ open, games, nodes, csrfToken, onClose, onCreated, copy }: { open: boolean; games: GameDefinition[]; nodes: Node[]; csrfToken: string; onClose: () => void; onCreated: (operation: Operation) => Promise<void>; copy: ServersCopy }) {
  const { locale } = useI18n();
  const eligibleGames = useMemo(() => games.filter((game) => game.status === "approved" && game.runnable), [games]);
  const [name, setName] = useState("");
  const [gameID, setGameID] = useState("");
  const [nodeID, setNodeID] = useState(nodes.find((node) => node.condition === "available")?.id ?? "");
  const [memory, setMemory] = useState(4096);
  const [disk, setDisk] = useState(25);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (eligibleGames.length && !eligibleGames.some((game) => game.id === gameID)) setGameID(eligibleGames[0].id);
    if (!eligibleGames.length && gameID) setGameID("");
    if (nodes.length && !nodeID) setNodeID(nodes.find((node) => node.condition === "available")?.id ?? "");
  }, [eligibleGames, gameID, nodeID, nodes]);
  const selectedGame = eligibleGames.find((game) => game.id === gameID);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try {
      if (!selectedGame) throw new Error(games.some((game) => game.id === gameID) ? copy.gameUnavailable : copy.gameMissing);
      const operation = await api.createServer({ name, gameDefinitionId: gameID, gameBundleDigest: selectedGame.bundleDigest, nodeId: nodeID, memoryMb: memory, diskGb: disk }, csrfToken);
      await onCreated(operation); setName("");
    } catch (reason) { setError(reason instanceof Error ? reason.message : copy.createFailed); } finally { setBusy(false); }
  };
  return <Modal open={open} title={copy.createTitle} description={copy.createDescription} onClose={onClose} footer={<><button className="button secondary" type="button" onClick={onClose}>{copy.cancel}</button><button className="button primary" form="create-server-form" disabled={busy || !name || !gameID || !nodeID || !selectedGame}>{busy ? copy.provisioning : copy.create}<ChevronRight size={16} /></button></>}><form id="create-server-form" className="modal-form" onSubmit={submit}>{!eligibleGames.length && <div className="form-error" role="status">{copy.noRunnableGames}</div>}<label>{copy.gameRelease}<select value={gameID} onChange={(event) => setGameID(event.target.value)} disabled={!eligibleGames.length}><option value="">{copy.noRunnableGames}</option>{eligibleGames.map((game) => <option key={game.id} value={game.id}>{game.name} {game.gameVersion} / {copy.definition} {game.version}</option>)}</select>{selectedGame && <small className="field-hint">{localizedGameSummary(selectedGame, locale)}</small>}</label><label>{copy.serverName}<input value={name} onChange={(event) => setName(event.target.value)} placeholder={copy.namePlaceholder} autoComplete="off" required disabled={!eligibleGames.length} /></label><label>{copy.node}<select value={nodeID} onChange={(event) => setNodeID(event.target.value)} disabled={!eligibleGames.length}>{nodes.map((node) => <option key={node.id} value={node.id} disabled={node.condition !== "available"}>{node.name} / {nodeLabel[node.condition]}</option>)}</select></label><div className="form-two-col"><label>{copy.memoryMb}<input type="number" min={512} max={131072} step={256} value={memory} onChange={(event) => setMemory(Number(event.target.value))} disabled={!eligibleGames.length} /></label><label>{copy.diskGb}<input type="number" min={1} max={2048} value={disk} onChange={(event) => setDisk(Number(event.target.value))} disabled={!eligibleGames.length} /></label></div>{error && <div className="form-error" role="alert">{error}</div>}</form></Modal>;
}
