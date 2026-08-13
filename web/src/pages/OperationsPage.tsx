import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import {
  Activity,
  ArrowUpRight,
  Ban,
  CheckCircle2,
  CircleAlert,
  Clock3,
  DatabaseBackup,
  ListFilter,
  Power,
  RefreshCw,
  RotateCcw,
  Search,
  ServerCog,
  Settings2,
  Trash2,
  Workflow,
} from "lucide-react";
import { ErrorState, LoadingState } from "../components/PageState";
import { StatusBadge } from "../components/StatusBadge";
import { api } from "../lib/api";
import { formatDateTime, relativeTime } from "../lib/format";
import type { Operation, Server } from "../lib/types";
import { type LocalizedCopy, useCopy } from "../i18n/I18n";

type OperationFilter = "all" | "active" | "failures" | "completed";

const activeStatuses = new Set<Operation["status"]>(["queued", "leased", "running"]);

interface OperationsCopy {
  loadError: string;
  loading: string;
  eyebrow: string;
  title: string;
  description: string;
  syncing: string;
  stale: string;
  updated: (value: string) => string;
  awaiting: string;
  refresh: string;
  summaryAria: string;
  summary: { total: string; active: string; failed: string; completed: string };
  liveRefreshFailed: string;
  partialSnapshot: string;
  retry: string;
  filterAria: string;
  filters: Record<OperationFilter, string>;
  searchPlaceholder: string;
  searchAria: string;
  lookupWarning: string;
  columns: { operation: string; state: string; execution: string; updated: string };
  emptyTitle: string;
  emptyAccepted: string;
  emptyFiltered: string;
  attempt: string;
  generation: string;
  complete: string;
  retryable: string;
  terminal: string;
  status: Record<Operation["status"], string>;
  operationType: Record<Operation["type"], string>;
}

const operationsCopy: LocalizedCopy<OperationsCopy> = {
  "zh-CN": { loadError: "无法刷新后台任务。", loading: "正在加载后台任务", eyebrow: "运行管理 / 后台任务", title: "后台任务", description: "查看服务器创建、启停、备份和状态同步等异步任务的执行进度。", syncing: "同步中", stale: "数据可能已过期", updated: (value) => `更新于 ${value}`, awaiting: "等待首次同步", refresh: "刷新", summaryAria: "后台任务状态摘要", summary: { total: "全部", active: "进行中", failed: "失败", completed: "已完成" }, liveRefreshFailed: "自动刷新失败。", partialSnapshot: "当前显示上一次成功获取的数据。", retry: "重试", filterAria: "按状态筛选后台任务", filters: { all: "全部", active: "进行中", failures: "失败", completed: "已完成" }, searchPlaceholder: "搜索任务或服务器", searchAria: "搜索后台任务", lookupWarning: "暂时无法获取服务器名称，已改为显示服务器 ID。", columns: { operation: "任务", state: "状态", execution: "执行信息", updated: "更新时间" }, emptyTitle: "当前没有后台任务。", emptyAccepted: "新的服务器任务会显示在这里。", emptyFiltered: "没有符合当前筛选条件的任务。", attempt: "尝试次数", generation: "配置版本", complete: "已完成", retryable: "可以重试", terminal: "已结束", status: { queued: "等待中", leased: "已领取", running: "执行中", succeeded: "已完成", failed: "失败" }, operationType: { provision: "创建服务器", start: "启动服务器", stop: "停止服务器", restart: "重启服务器", kill: "强制终止服务器", backup: "创建备份", restore: "恢复备份", "backup-delete": "删除备份", delete: "删除服务器", reconcile: "同步服务器状态" } },
  en: { loadError: "Unable to refresh operations.", loading: "Loading operation stream", eyebrow: "CONTROL ROOM / ASYNC WORK", title: "Operations", description: "Lifecycle work across every server in your current access scope.", syncing: "Syncing", stale: "Stale snapshot", updated: (value) => `Updated ${value}`, awaiting: "Awaiting sync", refresh: "Refresh", summaryAria: "Operation status summary", summary: { total: "Total", active: "Active", failed: "Failed", completed: "Completed" }, liveRefreshFailed: "Live refresh failed.", partialSnapshot: "Partial snapshot.", retry: "Retry", filterAria: "Operation status filter", filters: { all: "All", active: "Active", failures: "Failures", completed: "Completed" }, searchPlaceholder: "Search operation or server", searchAria: "Search operations", lookupWarning: "Server names could not be refreshed; identifiers are shown instead.", columns: { operation: "Operation", state: "State", execution: "Execution", updated: "Updated" }, emptyTitle: "No operations in this view.", emptyAccepted: "Accepted server work will appear here.", emptyFiltered: "The current filter has no matching work.", attempt: "attempt", generation: "Configuration version", complete: "% complete", retryable: "Retryable", terminal: "Terminal", status: { queued: "Queued", leased: "Leased", running: "Running", succeeded: "Succeeded", failed: "Failed" }, operationType: { provision: "Provision server", start: "Start server", stop: "Stop server", restart: "Restart server", kill: "Force terminate server", backup: "Create backup", restore: "Restore backup", "backup-delete": "Delete backup", delete: "Delete server", reconcile: "Reconcile server" } },
  ja: { loadError: "バックグラウンドタスクを更新できません。", loading: "バックグラウンドタスクを読み込み中", eyebrow: "運用管理 / バックグラウンドタスク", title: "バックグラウンドタスク", description: "サーバーの作成、電源操作、バックアップ、状態同期の進行状況を確認します。", syncing: "同期中", stale: "情報が古い可能性があります", updated: (value) => `更新：${value}`, awaiting: "初回同期を待機中", refresh: "更新", summaryAria: "バックグラウンドタスクの状態概要", summary: { total: "すべて", active: "実行中", failed: "失敗", completed: "完了" }, liveRefreshFailed: "自動更新に失敗しました。", partialSnapshot: "前回取得した情報を表示しています。", retry: "再試行", filterAria: "状態でタスクを絞り込む", filters: { all: "すべて", active: "実行中", failures: "失敗", completed: "完了" }, searchPlaceholder: "タスクまたはサーバーを検索", searchAria: "バックグラウンドタスクを検索", lookupWarning: "サーバー名を取得できないため、サーバー ID を表示します。", columns: { operation: "タスク", state: "状態", execution: "実行情報", updated: "更新時刻" }, emptyTitle: "現在バックグラウンドタスクはありません。", emptyAccepted: "新しいサーバータスクがここに表示されます。", emptyFiltered: "条件に一致するタスクはありません。", attempt: "試行回数", generation: "設定バージョン", complete: "% 完了", retryable: "再試行可能", terminal: "終了", status: { queued: "待機中", leased: "取得済み", running: "実行中", succeeded: "完了", failed: "失敗" }, operationType: { provision: "サーバーを作成", start: "サーバーを起動", stop: "サーバーを停止", restart: "サーバーを再起動", kill: "サーバーを強制終了", backup: "バックアップを作成", restore: "バックアップを復元", "backup-delete": "バックアップを削除", delete: "サーバーを削除", reconcile: "サーバー状態を同期" } },
  ko: { loadError: "백그라운드 작업을 새로 고칠 수 없습니다.", loading: "백그라운드 작업을 불러오는 중", eyebrow: "운영 관리 / 백그라운드 작업", title: "백그라운드 작업", description: "서버 생성, 전원 제어, 백업과 상태 동기화 작업의 진행 상황을 확인합니다.", syncing: "동기화 중", stale: "정보가 오래되었을 수 있습니다", updated: (value) => `업데이트: ${value}`, awaiting: "첫 동기화 대기 중", refresh: "새로 고침", summaryAria: "백그라운드 작업 상태 요약", summary: { total: "전체", active: "진행 중", failed: "실패", completed: "완료" }, liveRefreshFailed: "자동 새로 고침에 실패했습니다.", partialSnapshot: "마지막으로 불러온 정보를 표시합니다.", retry: "다시 시도", filterAria: "상태별 백그라운드 작업 필터", filters: { all: "전체", active: "진행 중", failures: "실패", completed: "완료" }, searchPlaceholder: "작업 또는 서버 검색", searchAria: "백그라운드 작업 검색", lookupWarning: "서버 이름을 불러올 수 없어 서버 ID를 표시합니다.", columns: { operation: "작업", state: "상태", execution: "실행 정보", updated: "업데이트 시간" }, emptyTitle: "현재 백그라운드 작업이 없습니다.", emptyAccepted: "새 서버 작업이 여기에 표시됩니다.", emptyFiltered: "조건과 일치하는 작업이 없습니다.", attempt: "시도 횟수", generation: "설정 버전", complete: "% 완료", retryable: "재시도 가능", terminal: "종료", status: { queued: "대기 중", leased: "할당됨", running: "실행 중", succeeded: "완료", failed: "실패" }, operationType: { provision: "서버 생성", start: "서버 시작", stop: "서버 중지", restart: "서버 재시작", kill: "서버 강제 종료", backup: "백업 생성", restore: "백업 복원", "backup-delete": "백업 삭제", delete: "서버 삭제", reconcile: "서버 상태 동기화" } },
};

const operationsHeadingCopy: LocalizedCopy<Pick<OperationsCopy, "eyebrow" | "title" | "description" | "operationType">> = {
  "zh-CN": {
    eyebrow: "运维 / 异步任务",
    title: "任务队列",
    description: "跟踪服务器创建、电源操作、备份与状态同步任务的执行进度。",
    operationType: { provision: "创建服务器", start: "启动服务器", stop: "停止服务器", restart: "重启服务器", kill: "强制终止服务器", backup: "创建备份", restore: "恢复备份", "backup-delete": "删除备份", delete: "删除服务器", reconcile: "同步服务器状态" },
  },
  en: {
    eyebrow: "OPERATIONS / ASYNC TASKS",
    title: "Task queue",
    description: "Track server creation, power actions, backups, and state synchronization.",
    operationType: { provision: "Create server", start: "Start server", stop: "Stop server", restart: "Restart server", kill: "Force terminate server", backup: "Create backup", restore: "Restore backup", "backup-delete": "Delete backup", delete: "Delete server", reconcile: "Synchronize server state" },
  },
  ja: {
    eyebrow: "運用 / 非同期タスク",
    title: "タスクキュー",
    description: "サーバー作成、電源操作、バックアップ、状態同期の進行状況を確認します。",
    operationType: { provision: "サーバーを作成", start: "サーバーを起動", stop: "サーバーを停止", restart: "サーバーを再起動", kill: "サーバーを強制終了", backup: "バックアップを作成", restore: "バックアップを復元", "backup-delete": "バックアップを削除", delete: "サーバーを削除", reconcile: "サーバー状態を同期" },
  },
  ko: {
    eyebrow: "운영 / 비동기 작업",
    title: "작업 대기열",
    description: "서버 생성, 전원 제어, 백업 및 상태 동기화 작업의 진행 상황을 확인합니다.",
    operationType: { provision: "서버 생성", start: "서버 시작", stop: "서버 중지", restart: "서버 재시작", kill: "서버 강제 종료", backup: "백업 생성", restore: "백업 복원", "backup-delete": "백업 삭제", delete: "서버 삭제", reconcile: "서버 상태 동기화" },
  },
};

const operationOpenCopy: LocalizedCopy<(name: string) => string> = {
  "zh-CN": (name) => `打开服务器 ${name}`,
  en: (name) => `Open server ${name}`,
  ja: (name) => `サーバー ${name} を開く`,
  ko: (name) => `서버 ${name} 열기`,
};

export function OperationsPage() {
  const copy = useCopy(operationsCopy);
  const headingCopy = useCopy(operationsHeadingCopy);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [filter, setFilter] = useState<OperationFilter>("all");
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");
  const [lookupWarning, setLookupWarning] = useState("");
  const [lastLoadedAt, setLastLoadedAt] = useState<string | null>(null);
  const mounted = useRef(true);
  const inFlight = useRef(false);

  const load = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setRefreshing(true);
    try {
      const [operationResult, serverResult] = await Promise.allSettled([api.operations(), api.servers()]);
      if (!mounted.current) return;
      if (operationResult.status === "fulfilled") {
        setOperations(operationResult.value);
        setError("");
        setLastLoadedAt(new Date().toISOString());
      } else {
        setError(messageFor(operationResult.reason, copy.loadError));
      }
      if (serverResult.status === "fulfilled") {
        setServers(serverResult.value);
        setLookupWarning("");
      } else {
        setLookupWarning(copy.lookupWarning);
      }
    } finally {
      inFlight.current = false;
      if (mounted.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [copy.loadError]);

  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);

  const activeCount = operations.filter((operation) => activeStatuses.has(operation.status)).length;
  useEffect(() => {
    if (activeCount === 0) return undefined;
    const timer = window.setInterval(() => void load(), 5_000);
    return () => window.clearInterval(timer);
  }, [activeCount, load]);

  const serverById = useMemo(() => new Map(servers.map((server) => [server.id, server])), [servers]);
  const counts = useMemo(() => ({
    all: operations.length,
    active: operations.filter((operation) => activeStatuses.has(operation.status)).length,
    failures: operations.filter((operation) => operation.status === "failed").length,
    completed: operations.filter((operation) => operation.status === "succeeded").length,
  }), [operations]);
  const visible = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return operations.filter((operation) => {
      const matchesFilter = filter === "all"
        || (filter === "active" && activeStatuses.has(operation.status))
        || (filter === "failures" && operation.status === "failed")
        || (filter === "completed" && operation.status === "succeeded");
      if (!matchesFilter) return false;
      if (!normalizedQuery) return true;
      const server = serverById.get(operation.serverId);
      return [operation.id, operation.type, operation.status, operation.checkpoint, operation.serverId, server?.name, server?.nodeName]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(normalizedQuery));
    });
  }, [filter, operations, query, serverById]);

  if (loading && operations.length === 0) return <section className="page"><LoadingState label={copy.loading} /></section>;
  if (error && operations.length === 0) return <section className="page"><ErrorState message={error} onRetry={() => void load()} /></section>;

  return (
    <section className="page operations-page">
      <div className="page-heading page-heading-wide">
        <div>
          <h1>{headingCopy.title}</h1>
          <p className="lede">{headingCopy.description}</p>
        </div>
        <div className="heading-actions">
          <span className={`refresh-note${error ? " is-stale" : ""}`} aria-live="polite">
            <i />{refreshing ? copy.syncing : error ? copy.stale : lastLoadedAt ? copy.updated(relativeTime(lastLoadedAt)) : copy.awaiting}
          </span>
          <button className="button secondary" onClick={() => void load()} disabled={refreshing}>
            <RefreshCw size={16} className={refreshing ? "is-spinning" : ""} />{copy.refresh}
          </button>
        </div>
      </div>

      <div className="operation-summary" aria-label={copy.summaryAria}>
        <SummaryCell icon={<Workflow size={17} />} label={copy.summary.total} value={counts.all} tone="neutral" />
        <SummaryCell icon={<Activity size={17} />} label={copy.summary.active} value={counts.active} tone="active" />
        <SummaryCell icon={<CircleAlert size={17} />} label={copy.summary.failed} value={counts.failures} tone="danger" />
        <SummaryCell icon={<CheckCircle2 size={17} />} label={copy.summary.completed} value={counts.completed} tone="success" />
      </div>

      {(error || lookupWarning) && (
        <div className="operations-warning" role="status">
          <CircleAlert size={17} />
          <span><strong>{error ? copy.liveRefreshFailed : copy.partialSnapshot}</strong>{error || lookupWarning}</span>
          <button type="button" onClick={() => void load()}>{copy.retry}</button>
        </div>
      )}

      <div className="toolbar-row operations-toolbar">
        <div className="segmented-control" role="tablist" aria-label={copy.filterAria}>
          <FilterTab active={filter === "all"} count={counts.all} label={copy.filters.all} onSelect={() => setFilter("all")} />
          <FilterTab active={filter === "active"} count={counts.active} icon={<Activity size={14} />} label={copy.filters.active} onSelect={() => setFilter("active")} />
          <FilterTab active={filter === "failures"} count={counts.failures} icon={<CircleAlert size={14} />} label={copy.filters.failures} onSelect={() => setFilter("failures")} />
          <FilterTab active={filter === "completed"} count={counts.completed} icon={<CheckCircle2 size={14} />} label={copy.filters.completed} onSelect={() => setFilter("completed")} />
        </div>
        <label className="search-input operations-search">
          <Search size={16} />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={copy.searchPlaceholder} aria-label={copy.searchAria} />
        </label>
      </div>

      <div className="panel operations-panel">
        <div className="operation-list-head" aria-hidden="true">
          <span>{copy.columns.operation}</span><span>{copy.columns.state}</span><span>{copy.columns.execution}</span><span>{copy.columns.updated}</span><span />
        </div>
        <div className="operation-list">
          {visible.map((operation) => (
            <OperationRow key={operation.id} operation={operation} server={serverById.get(operation.serverId)} copy={copy} />
          ))}
          {visible.length === 0 && (
            <div className="empty-state operation-empty">
              <ListFilter size={26} />
              <strong>{copy.emptyTitle}</strong>
              <span>{operations.length === 0 ? copy.emptyAccepted : copy.emptyFiltered}</span>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function FilterTab({ active, count, icon, label, onSelect }: { active: boolean; count: number; icon?: React.ReactNode; label: string; onSelect: () => void }) {
  return <button className={active ? "active" : ""} onClick={onSelect} role="tab" aria-selected={active}>{icon}{label}<span>{count}</span></button>;
}

function SummaryCell({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: number; tone: string }) {
  return <div className={`operation-summary-cell summary-${tone}`}><span className="operation-summary-icon">{icon}</span><span><small>{label}</small><strong>{value}</strong></span></div>;
}

function OperationRow({ operation, server, copy }: { operation: Operation; server?: Server; copy: OperationsCopy }) {
  const openServer = useCopy(operationOpenCopy);
  const terms = useCopy(operationsHeadingCopy);
  const active = activeStatuses.has(operation.status);
  const serverName = server?.name ?? shortId(operation.serverId);
  const nodeName = server?.nodeId === operation.nodeId ? server.nodeName : shortId(operation.nodeId);
  const tone = operation.status === "succeeded" ? "success" : operation.status === "failed" ? "danger" : active ? "warning" : "neutral";
  return (
    <article className={`operation-row operation-${operation.status}`}>
      <div className="operation-kind">
        <span className="operation-glyph"><OperationGlyph type={operation.type} /></span>
        <span>
          <strong>{terms.operationType[operation.type]}</strong>
          <small><code translate="no">{shortId(operation.id)}</code> / {copy.generation} <span translate="no">{operation.generation}</span></small>
        </span>
      </div>
      <div className="operation-state">
        <div className="operation-state-line">
          <StatusBadge tone={tone} pulse={active}>{copy.status[operation.status]}</StatusBadge>
          <span>{operation.progress}%</span>
        </div>
        <div className="operation-progress" aria-label={`${operation.progress}${copy.complete}`}><i style={{ width: `${Math.max(0, Math.min(100, operation.progress))}%` }} /></div>
        <small translate="no">{operation.checkpoint}</small>
      </div>
      <div className="operation-execution">
        <span><ServerCog size={13} /><strong translate="no">{serverName}</strong></span>
        <span><Workflow size={13} /><span translate="no">{nodeName}</span></span>
        <span><RotateCcw size={13} />{copy.attempt} <span translate="no">{operation.attempt} / {operation.maxAttempts}</span></span>
      </div>
      <div className="operation-time">
        <strong>{relativeTime(operation.updatedAt)}</strong>
        <small><Clock3 size={12} />{formatDateTime(operation.updatedAt)}</small>
      </div>
      <Link className="icon-link operation-open" to={`/servers/${operation.serverId}`} aria-label={openServer(serverName)} title={openServer(serverName)}>
        <ArrowUpRight size={16} />
      </Link>
      {operation.error && (
        <div className="operation-error" role="alert">
          <CircleAlert size={16} />
          <span translate="no"><strong>{operation.error.code}</strong>{operation.error.message}</span>
          <small>{operation.error.retryable ? copy.retryable : copy.terminal}</small>
        </div>
      )}
    </article>
  );
}

function OperationGlyph({ type }: { type: Operation["type"] }) {
  if (type === "backup" || type === "restore") return <DatabaseBackup size={17} />;
  if (type === "backup-delete" || type === "delete") return <Trash2 size={17} />;
  if (type === "reconcile") return <Settings2 size={17} />;
  if (type === "kill") return <Ban size={17} />;
  if (["start", "stop", "restart"].includes(type)) return <Power size={17} />;
  return <ServerCog size={17} />;
}

function shortId(value: string): string {
  return value.length > 14 ? `${value.slice(0, 8)}...${value.slice(-4)}` : value;
}

function messageFor(reason: unknown, fallback: string): string {
  return reason instanceof Error ? reason.message : fallback;
}
