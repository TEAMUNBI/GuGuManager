import type { Operation } from "./types";
import { getActiveLocale, type LocalizedCopy } from "../i18n/I18n";

type OperationReader = (operationId: string, signal?: AbortSignal) => Promise<Operation>;
type PollWait = (signal?: AbortSignal) => Promise<void>;
type OperationUpdate = (operation: Operation) => void;

interface PollOperationOptions {
  signal?: AbortSignal;
  maxConsecutiveFailures?: number;
}

const terminalStatuses = new Set<Operation["status"]>(["succeeded", "failed", "canceled"]);

const operationTypeLabels: LocalizedCopy<Record<Operation["type"], string>> = {
  "zh-CN": { provision: "创建服务器", start: "启动服务器", stop: "停止服务器", restart: "重启服务器", kill: "强制终止服务器", backup: "创建备份", restore: "恢复备份", "backup-delete": "删除备份", delete: "删除服务器", reconcile: "同步服务器状态" },
  en: { provision: "Provision server", start: "Start server", stop: "Stop server", restart: "Restart server", kill: "Force terminate server", backup: "Create backup", restore: "Restore backup", "backup-delete": "Delete backup", delete: "Delete server", reconcile: "Reconcile server" },
  ja: { provision: "サーバーをプロビジョニング", start: "サーバーを起動", stop: "サーバーを停止", restart: "サーバーを再起動", kill: "サーバーを強制終了", backup: "バックアップを作成", restore: "バックアップを復元", "backup-delete": "バックアップを削除", delete: "サーバーを削除", reconcile: "サーバー状態を照合" },
  ko: { provision: "서버 프로비저닝", start: "서버 시작", stop: "서버 중지", restart: "서버 재시작", kill: "서버 강제 종료", backup: "백업 만들기", restore: "백업 복원", "backup-delete": "백업 삭제", delete: "서버 삭제", reconcile: "서버 상태 조정" },
};

export const operationTypeLabel = new Proxy(operationTypeLabels.en, {
  get: (_target, property) => typeof property === "string"
    ? operationTypeLabels[getActiveLocale()][property as Operation["type"]]
    : Reflect.get(operationTypeLabels.en, property),
});

export function operationFailureMessage(operation: Operation, fallback: string): string {
  const message = operation.error?.message.trim();
  return message || fallback;
}

const abortError = () => {
  const error = new Error("Operation polling was canceled");
  error.name = "AbortError";
  return error;
};
const throwIfAborted = (signal?: AbortSignal) => {
  if (signal?.aborted) throw abortError();
};
const defaultWait: PollWait = (signal) => new Promise((resolve, reject) => {
  const onAbort = () => {
    window.clearTimeout(timer);
    reject(abortError());
  };
  const timer = window.setTimeout(() => {
    signal?.removeEventListener("abort", onAbort);
    resolve();
  }, 600);
  if (signal?.aborted) onAbort();
  else signal?.addEventListener("abort", onAbort, { once: true });
});

export async function pollOperation(
  initial: Operation,
  read: OperationReader,
  wait: PollWait = defaultWait,
  onUpdate: OperationUpdate = () => undefined,
  options: PollOperationOptions = {},
): Promise<Operation> {
  let current = initial;
  let consecutiveFailures = 0;
  const maxConsecutiveFailures = Math.max(1, options.maxConsecutiveFailures ?? 5);
  while (!terminalStatuses.has(current.status)) {
    throwIfAborted(options.signal);
    await wait(options.signal);
    throwIfAborted(options.signal);
    try {
      const next = await read(current.id, options.signal);
      throwIfAborted(options.signal);
      current = next;
      consecutiveFailures = 0;
      onUpdate(current);
    } catch (error) {
      throwIfAborted(options.signal);
      consecutiveFailures += 1;
      if (consecutiveFailures >= maxConsecutiveFailures) throw error;
    }
  }
  return current;
}
