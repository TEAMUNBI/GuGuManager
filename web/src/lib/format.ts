import { getActiveLocale, type LocalizedCopy } from "../i18n/I18n";

export function formatBytes(value: number, precision = 1): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : precision)} ${units[index]}`;
}

export function formatPercent(value: number): string {
  return `${Math.round(value)}%`;
}

export function relativeTime(value: string): string {
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(getActiveLocale(), { numeric: "auto" });
  if (Math.abs(seconds) < 60) return formatter.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, "hour");
  return formatter.format(Math.round(hours / 24), "day");
}

const snapshotCopy: LocalizedCopy<{ awaiting: string; stale: string; updated: string }> = {
  "zh-CN": { awaiting: "等待同步", stale: "已过期", updated: "已更新" },
  en: { awaiting: "Awaiting sync", stale: "Stale", updated: "Updated" },
  ja: { awaiting: "同期を待機中", stale: "期限切れ", updated: "更新済み" },
  ko: { awaiting: "동기화 대기 중", stale: "오래된 정보", updated: "업데이트됨" },
};

export function snapshotLabel(lastLoadedAt: string | null, stale: boolean): string {
  const copy = snapshotCopy[getActiveLocale()];
  if (!lastLoadedAt) return copy.awaiting;
  return `${stale ? copy.stale : copy.updated} · ${relativeTime(lastLoadedAt)}`;
}

export function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat(getActiveLocale(), { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value));
}

function localizedRecord<T extends Record<string, string>>(records: LocalizedCopy<T>): Record<string, string> {
  return new Proxy(records.en, {
    get: (_target, property) => typeof property === "string"
      ? records[getActiveLocale()][property] ?? records.en[property]
      : Reflect.get(records.en, property),
  });
}

export const powerLabel = localizedRecord({
  "zh-CN": { running: "运行中", stopped: "已停止", starting: "启动中", stopping: "停止中", unknown: "未知", unhealthy: "异常" },
  en: { running: "Running", stopped: "Stopped", starting: "Starting", stopping: "Stopping", unknown: "Unknown", unhealthy: "Unhealthy" },
  ja: { running: "稼働中", stopped: "停止済み", starting: "起動中", stopping: "停止中", unknown: "不明", unhealthy: "異常" },
  ko: { running: "실행 중", stopped: "중지됨", starting: "시작 중", stopping: "중지 중", unknown: "알 수 없음", unhealthy: "비정상" },
} satisfies LocalizedCopy<Record<string, string>>);

export const nodeLabel = localizedRecord({
  "zh-CN": { available: "在线", offline: "离线", maintenance: "维护中" },
  en: { available: "Available", offline: "Offline", maintenance: "Maintenance" },
  ja: { available: "利用可能", offline: "オフライン", maintenance: "メンテナンス中" },
  ko: { available: "사용 가능", offline: "오프라인", maintenance: "유지 보수 중" },
} satisfies LocalizedCopy<Record<string, string>>);

export const actionLabel = localizedRecord({
  "zh-CN": { "server.power.start": "启动服务器", "server.power.stop": "停止服务器", "server.power.restart": "重启服务器", "server.power.kill": "强制终止服务器", "server.create": "创建服务器", "backup.create": "创建备份", "catalog.approve": "审核游戏模板", "node.heartbeat": "节点上报心跳", "server.reconcile": "同步服务器状态", "auth.login": "管理员登录", "auth.logout": "管理员退出", "console.command": "发送控制台命令" },
  en: { "server.power.start": "Start server", "server.power.stop": "Stop server", "server.power.restart": "Restart server", "server.power.kill": "Force terminate", "server.create": "Create server", "backup.create": "Create backup", "catalog.approve": "Approve game bundle", "node.heartbeat": "Node heartbeat", "server.reconcile": "Reconcile state", "auth.login": "Administrator sign in", "auth.logout": "Administrator sign out", "console.command": "Send console command" },
  ja: { "server.power.start": "サーバーを起動", "server.power.stop": "サーバーを停止", "server.power.restart": "サーバーを再起動", "server.power.kill": "強制終了", "server.create": "サーバーを作成", "backup.create": "バックアップを作成", "catalog.approve": "ゲームバンドルを承認", "node.heartbeat": "ノードのハートビート", "server.reconcile": "状態を照合", "auth.login": "管理者がサインイン", "auth.logout": "管理者がサインアウト", "console.command": "コンソールコマンドを送信" },
  ko: { "server.power.start": "서버 시작", "server.power.stop": "서버 중지", "server.power.restart": "서버 재시작", "server.power.kill": "강제 종료", "server.create": "서버 만들기", "backup.create": "백업 만들기", "catalog.approve": "게임 번들 승인", "node.heartbeat": "노드 하트비트", "server.reconcile": "상태 조정", "auth.login": "관리자 로그인", "auth.logout": "관리자 로그아웃", "console.command": "콘솔 명령 전송" },
} satisfies LocalizedCopy<Record<string, string>>);
