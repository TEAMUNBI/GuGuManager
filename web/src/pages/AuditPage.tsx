import { useEffect, useMemo, useState } from "react";
import { CalendarClock, CheckCircle2, ClipboardList, Clock3, Search, ShieldCheck, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { AuditEvent } from "../lib/types";
import { actionLabel, formatDateTime, relativeTime } from "../lib/format";
import { LoadingState, ErrorState } from "../components/PageState";
import { type LocalizedCopy, useCopy } from "../i18n/I18n";

type AuditResult = AuditEvent["result"];
type AuditFilter = "all" | AuditResult;

interface AuditCopy {
  controlPlane: string;
  targetType: Record<string, string>;
  page: {
    eyebrow: string;
    title: string;
    description: string;
    loading: string;
    loadError: string;
  };
  signal: {
    title: string;
    description: (count: number) => string;
    storage: string;
    inMemory: string;
  };
  filters: {
    aria: string;
    all: string;
    accepted: string;
    success: string;
    failure: string;
  };
  search: {
    placeholder: string;
    aria: string;
  };
  table: {
    event: string;
    actor: string;
    target: string;
    result: string;
    time: string;
    operation: string;
  };
  result: Record<AuditResult, string>;
  empty: {
    title: string;
    description: string;
  };
}

const auditCopy: LocalizedCopy<AuditCopy> = {
  "zh-CN": {
    controlPlane: "管理服务",
    targetType: { server: "服务器", game_definition: "游戏模板" },
    page: {
      eyebrow: "系统管理 / 审计日志",
      title: "审计日志",
      description: "记录重要操作的执行人、目标、结果和发生时间。",
      loading: "正在读取审计日志",
      loadError: "无法加载审计日志",
    },
    signal: {
      title: "审计日志已就绪",
      description: (count) => `当前开发环境中共有 ${count} 条记录；日志链校验尚未启用。`,
      storage: "存储方式",
      inMemory: "临时内存存储",
    },
    filters: { aria: "按结果筛选审计日志", all: "全部", accepted: "已受理", success: "成功", failure: "失败" },
    search: { placeholder: "搜索执行人、操作或目标", aria: "搜索审计日志" },
    table: { event: "操作", actor: "执行人", target: "目标", result: "结果", time: "时间", operation: "任务 ID" },
    result: { accepted: "已受理", success: "成功", failure: "失败" },
    empty: { title: "没有符合条件的操作记录。", description: "请调整搜索内容或筛选条件。" },
  },
  en: {
    controlPlane: "Control plane",
    targetType: { server: "Server", game_definition: "Game template" },
    page: {
      eyebrow: "SYSTEM ADMIN / AUDIT LOG",
      title: "Audit log",
      description: "A record of important actions, who performed them, their target, and the outcome.",
      loading: "Loading audit log",
      loadError: "Unable to load audit log",
    },
    signal: {
      title: "Audit log ready",
      description: (count) => `${count} ${count === 1 ? "record is" : "records are"} available in this development environment; log-chain verification is not enabled yet.`,
      storage: "Storage",
      inMemory: "Temporary in-memory storage",
    },
    filters: { aria: "Filter audit log by result", all: "All", accepted: "Accepted", success: "Success", failure: "Failed" },
    search: { placeholder: "Search actor, action, or target", aria: "Search audit log" },
    table: { event: "Action", actor: "Actor", target: "Target", result: "Result", time: "Time", operation: "Task ID" },
    result: { accepted: "ACCEPTED", success: "SUCCESS", failure: "FAILED" },
    empty: { title: "No activity matches these filters.", description: "Change the search or result filter." },
  },
  ja: {
    controlPlane: "管理サービス",
    targetType: { server: "サーバー", game_definition: "ゲームテンプレート" },
    page: {
      eyebrow: "システム管理 / 監査ログ",
      title: "監査ログ",
      description: "重要な操作の実行者、対象、結果、実行時刻を記録します。",
      loading: "監査ログを読み込み中",
      loadError: "監査ログを読み込めません",
    },
    signal: {
      title: "監査ログを読み込みました",
      description: (count) => `現在の開発環境には ${count} 件の記録があります。ログチェーンの検証はまだ有効ではありません。`,
      storage: "保存方式",
      inMemory: "インメモリ一時ストレージ",
    },
    filters: { aria: "結果で監査ログを絞り込む", all: "すべて", accepted: "受付済み", success: "成功", failure: "失敗" },
    search: { placeholder: "実行者、操作、対象を検索", aria: "監査ログを検索" },
    table: { event: "操作", actor: "実行者", target: "対象", result: "結果", time: "時刻", operation: "タスク ID" },
    result: { accepted: "受付済み", success: "成功", failure: "失敗" },
    empty: { title: "条件に一致する監査ログはありません。", description: "検索語またはフィルターを変更してください。" },
  },
  ko: {
    controlPlane: "관리 서비스",
    targetType: { server: "서버", game_definition: "게임 템플릿" },
    page: {
      eyebrow: "시스템 관리 / 감사 로그",
      title: "감사 로그",
      description: "중요한 작업의 실행자, 대상, 결과와 실행 시간을 기록합니다.",
      loading: "감사 로그를 불러오는 중",
      loadError: "감사 로그를 불러올 수 없습니다",
    },
    signal: {
      title: "감사 로그를 불러왔습니다",
      description: (count) => `현재 개발 환경에 ${count}개의 기록이 있습니다. 로그 체인 검증은 아직 활성화되지 않았습니다.`,
      storage: "저장 방식",
      inMemory: "인메모리 임시 저장소",
    },
    filters: { aria: "결과별 감사 로그 필터", all: "전체", accepted: "접수됨", success: "성공", failure: "실패" },
    search: { placeholder: "실행자, 작업 또는 대상 검색", aria: "감사 로그 검색" },
    table: { event: "작업", actor: "실행자", target: "대상", result: "결과", time: "시간", operation: "작업 ID" },
    result: { accepted: "접수됨", success: "성공", failure: "실패" },
    empty: { title: "조건과 일치하는 감사 로그가 없습니다.", description: "검색어나 필터를 변경해 보세요." },
  },
};

export function AuditPage() {
  const copy = useCopy(auditCopy);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<AuditFilter>("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    api.audit()
      .then(setEvents)
      .catch((reason) => setError(reason instanceof Error ? reason.message : copy.page.loadError))
      .finally(() => setLoading(false));
  }, []);

  const visible = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return events.filter((event) => {
      if (result !== "all" && event.result !== result) return false;
      if (!normalizedQuery) return true;
      const localizedAction = actionLabel[event.action] ?? event.action;
      const localizedTargetType = copy.targetType[event.targetType] ?? event.targetType;
      return `${event.action} ${localizedAction} ${localizedTargetType} ${event.targetName} ${event.actorName}`.toLowerCase().includes(normalizedQuery);
    });
  }, [copy, events, query, result]);

  if (loading) return <section className="page"><LoadingState label={copy.page.loading} /></section>;
  if (error) return <section className="page"><ErrorState message={error} /></section>;

  return (
    <section className="page audit-page">
      <div className="page-heading page-heading-wide"><div><h1>{copy.page.title}</h1><p className="lede">{copy.page.description}</p></div></div>
      <div className="audit-signal"><div className="audit-signal-icon"><ShieldCheck size={22} /></div><div><strong>{copy.signal.title}</strong><span>{copy.signal.description(events.length)}</span></div><div className="audit-signal-side"><span>{copy.signal.storage}</span><b>{copy.signal.inMemory}</b></div></div>
      <div className="toolbar-row audit-toolbar">
        <div className="segmented-control" role="tablist" aria-label={copy.filters.aria}>
          <button className={result === "all" ? "active" : ""} onClick={() => setResult("all")} role="tab" aria-selected={result === "all"}>{copy.filters.all} <span>{events.length}</span></button>
          <button className={result === "accepted" ? "active" : ""} onClick={() => setResult("accepted")} role="tab" aria-selected={result === "accepted"}><Clock3 size={14} />{copy.filters.accepted}</button>
          <button className={result === "success" ? "active" : ""} onClick={() => setResult("success")} role="tab" aria-selected={result === "success"}><CheckCircle2 size={14} />{copy.filters.success}</button>
          <button className={result === "failure" ? "active" : ""} onClick={() => setResult("failure")} role="tab" aria-selected={result === "failure"}><XCircle size={14} />{copy.filters.failure}</button>
        </div>
        <label className="search-input"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={copy.search.placeholder} aria-label={copy.search.aria} /></label>
      </div>
      <div className="panel audit-panel">
        <div className="audit-table-head"><span>{copy.table.event}</span><span>{copy.table.actor}</span><span>{copy.table.target}</span><span>{copy.table.result}</span><span>{copy.table.time}</span><span>{copy.table.operation}</span></div>
        <div className="audit-event-list">
          {visible.map((event) => <div className="audit-event-row" key={event.id}>
            <div className="audit-event-action"><span className={`audit-icon audit-${event.result}`}>{event.result === "success" ? <CheckCircle2 size={16} /> : event.result === "accepted" ? <Clock3 size={16} /> : <XCircle size={16} />}</span><span><strong>{actionLabel[event.action] ?? event.action}</strong><small translate="no">{event.action}</small></span></div>
            <div className="audit-cell"><span className="avatar avatar-tiny">{event.actorName.slice(0, 1)}</span>{event.actorName}</div>
            <div className="audit-cell target-cell"><span>{event.targetName === "Control Plane" ? copy.controlPlane : event.targetName}</span><small>{copy.targetType[event.targetType] ?? event.targetType}</small></div>
            <div><span className={`result-pill result-${event.result}`}>{copy.result[event.result]}</span></div>
            <div className="audit-time"><strong>{relativeTime(event.createdAt)}</strong><small><CalendarClock size={12} />{formatDateTime(event.createdAt)}</small></div>
            <div className="audit-operation"><code translate="no">{event.operationId ? `${event.operationId.slice(0, 13)}...` : "-"}</code></div>
          </div>)}
          {!visible.length && <div className="empty-state"><ClipboardList size={25} /><strong>{copy.empty.title}</strong><span>{copy.empty.description}</span></div>}
        </div>
      </div>
    </section>
  );
}
