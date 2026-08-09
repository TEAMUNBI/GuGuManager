import { AlertCircle, RefreshCw } from "lucide-react";
import { type LocalizedCopy, useCopy } from "../i18n/I18n";

const pageStateCopy: LocalizedCopy<{ loading: string; error: string; retry: string }> = {
  "zh-CN": { loading: "正在载入", error: "无法加载数据", retry: "重试" },
  en: { loading: "Loading", error: "Unable to load data", retry: "Retry" },
  ja: { loading: "読み込み中", error: "データを読み込めません", retry: "再試行" },
  ko: { loading: "불러오는 중", error: "데이터를 불러올 수 없음", retry: "다시 시도" },
};

export function LoadingState({ label }: { label?: string }) {
  const copy = useCopy(pageStateCopy);
  return <div className="page-state"><span className="loading-rule" aria-hidden="true" /><span>{label ?? copy.loading}</span></div>;
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  const copy = useCopy(pageStateCopy);
  return <div className="page-state page-state-error"><AlertCircle size={24} /><div><strong>{copy.error}</strong><span>{message}</span></div>{onRetry && <button className="button secondary" onClick={onRetry}><RefreshCw size={16} />{copy.retry}</button>}</div>;
}
