import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, Archive, ArrowDownToLine, ArrowLeft, Braces, Check, ChevronRight, Circle, CircleAlert, Clipboard, Download, File, FileCode2, FilePlus2, FileText, Folder, FolderPlus, Gauge, HardDrive, KeyRound, LockKeyhole, MemoryStick, Move, Network, Plus, Play, Power, RefreshCw, RotateCcw, Save, Send, Server as ServerIcon, Settings2, ShieldCheck, Square, Star, TerminalSquare, Trash2, Users } from "lucide-react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../lib/api";
import type { Allocation, Backup, ConsoleLine, FileEntry, Operation, Server, ServerPermission, Startup, StartupValue, StartupVariable } from "../lib/types";
import { operationFailureMessage, pollOperation } from "../lib/operations";
import { formatBytes, nodeLabel, powerLabel } from "../lib/format";
import { ErrorState, LoadingState } from "../components/PageState";
import { MetricBars } from "../components/MetricBars";
import { StatusBadge, toneForNode, toneForPower } from "../components/StatusBadge";
import { Modal } from "../components/Modal";
import { FileEditor } from "../components/FileEditor";
import { useAppContext } from "../app/App";
import { isPowerControlLocked } from "../domain/power";
import { type LocalizedCopy, type Locale, useCopy, useI18n } from "../i18n/I18n";

type Tab = "overview" | "console" | "files" | "backups" | "network" | "startup" | "activity" | "settings";
type PowerAction = "start" | "stop" | "restart" | "kill";

function defineCopy<T>(copy: LocalizedCopy<T>): LocalizedCopy<T> {
  return copy;
}

const intlLocales: Record<Locale, string> = {
  "zh-CN": "zh-CN",
  en: "en-US",
  ja: "ja-JP",
  ko: "ko-KR",
};

function localizedDateTime(value: string, locale: Locale): string {
  return new Intl.DateTimeFormat(intlLocales[locale], {
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

function localizedRelativeTime(value: string, locale: Locale): string {
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(intlLocales[locale], { numeric: "auto" });
  if (Math.abs(seconds) < 60) return formatter.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, "hour");
  return formatter.format(Math.round(hours / 24), "day");
}

function formatConsoleTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "--:--:--";
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

const shellCopy = defineCopy({
  "zh-CN": {
    loadError: "无法加载服务器",
    loading: "正在进入服务器工作区",
    allServers: "所有服务器",
    nodeOffline: "节点离线",
    refreshServer: "刷新服务器",
    start: "启动",
    restart: "重启",
    stop: "停止",
    forceTerminate: "强制终止",
    permissionDenied: "无权访问此服务器页面",
    serverPages: "服务器页面",
    tabs: { overview: "概览", console: "控制台", files: "文件", backups: "备份", network: "网络", startup: "启动配置", activity: "任务记录", settings: "服务器详情" },
    powerStatus: { unknown: "未知", stopped: "已停止", starting: "启动中", running: "运行中", stopping: "停止中" },
    powerAction: { start: "启动", stop: "停止", restart: "重启", kill: "强制终止" },
    operationStatus: { queued: "等待中", leased: "已领取", dispatched: "已下发", running: "执行中", succeeded: "已完成", failed: "失败", canceled: "已取消" },
    powerAccepted: (action: string) => `${action}请求已受理`,
    powerCompleted: (action: string) => `${action}操作已完成`,
    powerTerminal: (action: string, status: string) => `${action}操作状态：${status}`,
    powerRequestFailed: "电源请求失败",
    terminateTitle: "强制终止服务器？",
    terminateDescription: "此操作会绕过游戏的正常关闭流程，并可能损坏正在写入的存档。",
    cancel: "取消",
    generation: "配置版本",
  },
  en: {
    loadError: "Unable to load the server",
    loading: "Entering server workspace",
    allServers: "All servers",
    nodeOffline: "Node offline",
    refreshServer: "Refresh server",
    start: "Start",
    restart: "Restart",
    stop: "Stop",
    forceTerminate: "Force terminate",
    permissionDenied: "You do not have permission to use this server page.",
    serverPages: "Server pages",
    tabs: { overview: "Overview", console: "Console", files: "Files", backups: "Backups", network: "Network", startup: "Startup", activity: "Task history", settings: "Server details" },
    powerStatus: { unknown: "Unknown", stopped: "Stopped", starting: "Starting", running: "Running", stopping: "Stopping" },
    powerAction: { start: "Start", stop: "Stop", restart: "Restart", kill: "Force terminate" },
    operationStatus: { queued: "Queued", leased: "Claimed", dispatched: "Dispatched", running: "Running", succeeded: "Succeeded", failed: "Failed", canceled: "Canceled" },
    powerAccepted: (action: string) => `${action} request accepted`,
    powerCompleted: (action: string) => `${action} completed`,
    powerTerminal: (action: string, status: string) => `${action} status: ${status}`,
    powerRequestFailed: "Power request failed",
    terminateTitle: "Force terminate server?",
    terminateDescription: "This bypasses the game's graceful shutdown path and can damage active saves.",
    cancel: "Cancel",
    generation: "configuration version",
  },
  ja: {
    loadError: "サーバーを読み込めません",
    loading: "サーバーワークスペースを開いています",
    allServers: "すべてのサーバー",
    nodeOffline: "ノードはオフラインです",
    refreshServer: "サーバーを更新",
    start: "起動",
    restart: "再起動",
    stop: "停止",
    forceTerminate: "強制終了",
    permissionDenied: "このサーバーページを使用する権限がありません。",
    serverPages: "サーバーページ",
    tabs: { overview: "概要", console: "コンソール", files: "ファイル", backups: "バックアップ", network: "ネットワーク", startup: "起動設定", activity: "タスク履歴", settings: "サーバー詳細" },
    powerStatus: { unknown: "不明", stopped: "停止済み", starting: "起動中", running: "稼働中", stopping: "停止中" },
    powerAction: { start: "起動", stop: "停止", restart: "再起動", kill: "強制終了" },
    operationStatus: { queued: "待機中", leased: "取得済み", dispatched: "送信済み", running: "実行中", succeeded: "完了", failed: "失敗", canceled: "キャンセル済み" },
    powerAccepted: (action: string) => `${action}リクエストを受け付けました`,
    powerCompleted: (action: string) => `${action}操作が完了しました`,
    powerTerminal: (action: string, status: string) => `${action}操作の状態：${status}`,
    powerRequestFailed: "電源操作に失敗しました",
    terminateTitle: "サーバーを強制終了しますか？",
    terminateDescription: "ゲームの通常終了処理を回避するため、書き込み中のセーブデータが破損する可能性があります。",
    cancel: "キャンセル",
    generation: "設定バージョン",
  },
  ko: {
    loadError: "서버를 불러올 수 없습니다",
    loading: "서버 작업 공간에 들어가는 중",
    allServers: "모든 서버",
    nodeOffline: "노드 오프라인",
    refreshServer: "서버 새로고침",
    start: "시작",
    restart: "재시작",
    stop: "중지",
    forceTerminate: "강제 종료",
    permissionDenied: "이 서버 페이지를 사용할 권한이 없습니다.",
    serverPages: "서버 페이지",
    tabs: { overview: "개요", console: "콘솔", files: "파일", backups: "백업", network: "네트워크", startup: "시작 설정", activity: "작업 기록", settings: "서버 세부 정보" },
    powerStatus: { unknown: "알 수 없음", stopped: "중지됨", starting: "시작 중", running: "실행 중", stopping: "중지 중" },
    powerAction: { start: "시작", stop: "중지", restart: "재시작", kill: "강제 종료" },
    operationStatus: { queued: "대기 중", leased: "할당됨", dispatched: "전송됨", running: "실행 중", succeeded: "완료", failed: "실패", canceled: "취소됨" },
    powerAccepted: (action: string) => `${action} 요청이 접수되었습니다`,
    powerCompleted: (action: string) => `${action} 작업이 완료되었습니다`,
    powerTerminal: (action: string, status: string) => `${action} 작업 상태: ${status}`,
    powerRequestFailed: "전원 요청에 실패했습니다",
    terminateTitle: "서버를 강제 종료할까요?",
    terminateDescription: "게임의 정상 종료 절차를 건너뛰므로 저장 중인 데이터가 손상될 수 있습니다.",
    cancel: "취소",
    generation: "설정 버전",
  },
});

const overviewCopy = defineCopy({
  "zh-CN": {
    cpuLoad: "CPU 负载", cpuTrend: "CPU 最近一小时趋势", lastHour: "最近 60 分钟", memory: "内存", memoryTrend: "内存最近一小时趋势", limit: (value: string) => `上限 ${value}`, players: "玩家", playersUnavailable: "暂无玩家遥测", developmentSnapshot: "实时采集 · 每 5 秒",
    resourceEyebrow: "资源用量", runtimeLimits: "资源配额", cpu: "CPU", disk: "磁盘", ofNodeShares: "节点配额占比",
    connectionEyebrow: "连接信息", allocation: "连接地址", copyAddress: "复制连接地址", node: "运行节点", game: "游戏", definition: "模板版本", owner: "所有者", observedGeneration: "已生效版本", lastUpdate: "最近更新",
  },
  en: {
    cpuLoad: "CPU load", cpuTrend: "CPU trend over the last hour", lastHour: "Last 60 minutes", memory: "Memory", memoryTrend: "Memory trend over the last hour", limit: (value: string) => `Limit ${value}`, players: "Players", playersUnavailable: "Player telemetry unavailable", developmentSnapshot: "Live · sampled every 5s",
    resourceEyebrow: "RESOURCE USAGE", runtimeLimits: "Resource limits", cpu: "CPU", disk: "Disk", ofNodeShares: "of node capacity",
    connectionEyebrow: "CONNECTION", allocation: "Connection address", copyAddress: "Copy address", node: "Node", game: "Game", definition: "Template version", owner: "Owner", observedGeneration: "Applied version", lastUpdate: "Last update",
  },
  ja: {
    cpuLoad: "CPU 負荷", cpuTrend: "直近 1 時間の CPU 推移", lastHour: "直近 60 分", memory: "メモリ", memoryTrend: "直近 1 時間のメモリ推移", limit: (value: string) => `上限 ${value}`, players: "プレイヤー", playersUnavailable: "プレイヤー情報を取得できません", developmentSnapshot: "リアルタイム収集 · 5 秒ごと",
    resourceEyebrow: "リソース使用量", runtimeLimits: "リソース上限", cpu: "CPU", disk: "ディスク", ofNodeShares: "ノード容量に対する使用率",
    connectionEyebrow: "接続情報", allocation: "接続先", copyAddress: "アドレスをコピー", node: "ノード", game: "ゲーム", definition: "テンプレートバージョン", owner: "所有者", observedGeneration: "適用済みバージョン", lastUpdate: "最終更新",
  },
  ko: {
    cpuLoad: "CPU 부하", cpuTrend: "최근 1시간 CPU 추이", lastHour: "최근 60분", memory: "메모리", memoryTrend: "최근 1시간 메모리 추이", limit: (value: string) => `한도 ${value}`, players: "플레이어", playersUnavailable: "플레이어 정보를 확인할 수 없음", developmentSnapshot: "실시간 수집 · 5초 간격",
    resourceEyebrow: "리소스 사용량", runtimeLimits: "리소스 한도", cpu: "CPU", disk: "디스크", ofNodeShares: "노드 용량 대비 사용률",
    connectionEyebrow: "연결 정보", allocation: "접속 주소", copyAddress: "주소 복사", node: "노드", game: "게임", definition: "템플릿 버전", owner: "소유자", observedGeneration: "적용된 버전", lastUpdate: "마지막 업데이트",
  },
});

const consoleCopy = defineCopy({
  "zh-CN": {
    sendFailed: "命令发送失败", stream: "控制台流", sequence: "序号", waiting: "正在等待控制台输出…", commandPlaceholder: "输入服务器命令", stoppedPlaceholder: "服务器必须处于运行状态", commandAria: "控制台命令", send: "发送命令", autoScroll: "自动滚动", clearConsole: "清空控制台",
    eyebrow: "实时输出", connection: "连接状态", transport: "传输方式", transportValue: "实时（Agent 日志流）", lastSequence: "最后序号", snapshot: "快照", lineCount: (count: number) => `${count} 行`, inputLimit: "输入上限", characterCount: (count: number) => `${count} 个字符`, scoped: "命令输入受当前权限范围限制。", authorization: "管理服务会在发送时再次检查操作权限。",
  },
  en: {
    sendFailed: "Command could not be sent", stream: "console stream", sequence: "seq", waiting: "Waiting for console output...", commandPlaceholder: "Enter a server command", stoppedPlaceholder: "Server must be running", commandAria: "Console command", send: "Send command", autoScroll: "Auto-scroll", clearConsole: "Clear console",
    eyebrow: "LIVE OUTPUT", connection: "Connection", transport: "Transport", transportValue: "Realtime (agent log stream)", lastSequence: "Last sequence", snapshot: "Snapshot", lineCount: (count: number) => `${count} ${count === 1 ? "line" : "lines"}`, inputLimit: "Input limit", characterCount: (count: number) => `${count} characters`, scoped: "Command input is restricted to your current permissions.", authorization: "The control plane checks authorization again before sending.",
  },
  ja: {
    sendFailed: "コマンドを送信できません", stream: "コンソールストリーム", sequence: "連番", waiting: "コンソール出力を待機しています…", commandPlaceholder: "サーバーコマンドを入力", stoppedPlaceholder: "サーバーを起動してください", commandAria: "コンソールコマンド", send: "コマンドを送信", autoScroll: "自動スクロール", clearConsole: "コンソールをクリア",
    eyebrow: "リアルタイム出力", connection: "接続状態", transport: "転送方式", transportValue: "リアルタイム（Agent ログストリーム）", lastSequence: "最終連番", snapshot: "スナップショット", lineCount: (count: number) => `${count} 行`, inputLimit: "入力上限", characterCount: (count: number) => `${count} 文字`, scoped: "コマンド入力は現在の権限範囲に限定されます。", authorization: "管理サービスが送信時に操作権限を再確認します。",
  },
  ko: {
    sendFailed: "명령을 전송할 수 없습니다", stream: "콘솔 스트림", sequence: "순번", waiting: "콘솔 출력을 기다리는 중…", commandPlaceholder: "서버 명령 입력", stoppedPlaceholder: "서버가 실행 중이어야 합니다", commandAria: "콘솔 명령", send: "명령 전송", autoScroll: "자동 스크롤", clearConsole: "콘솔 지우기",
    eyebrow: "실시간 출력", connection: "연결 상태", transport: "전송 방식", transportValue: "실시간 (Agent 로그 스트림)", lastSequence: "마지막 순번", snapshot: "스냅샷", lineCount: (count: number) => `${count}줄`, inputLimit: "입력 제한", characterCount: (count: number) => `${count}자`, scoped: "명령 입력은 현재 권한 범위로 제한됩니다.", authorization: "관리 서비스가 전송 전에 작업 권한을 다시 확인합니다.",
  },
});

const filesCopy = defineCopy({
  "zh-CN": {
    loadError: "文件列表加载失败", readFailed: "文件读取失败", duplicate: "名称为空或目标已经存在", directoryCreated: "目录已创建", fileCreated: "文件已创建", createFailed: "创建失败", fileSaved: "文件已保存", saveFailed: "文件保存失败", entryMoved: "项目已移动", moveFailed: "移动失败", entryDeleted: "项目已删除", deleteFailed: "删除失败",
    newFileAria: "新建文件", newDirectoryAria: "新建目录", refreshFilesAria: "刷新文件", newFileTitle: "新建文件", newDirectoryTitle: "新建目录", refreshTitle: "刷新",
    name: "名称", size: "大小", modified: "修改时间", actions: "操作", moveAria: (name: string) => `移动 ${name}`, deleteAria: (name: string) => `删除 ${name}`, openAria: (name: string) => `打开 ${name}`, moveTitle: "移动或重命名", deleteTitle: "删除", openTitle: "打开目录",
    unableDirectory: "无法加载此目录。", retry: "重试", emptyDirectory: "此目录为空。", emptyDetail: "此路径下没有文件。", entries: (count: number) => `${count} 个项目`, pathBoundary: "限定路径 / server-data",
    createDirectory: "新建目录", createFile: "新建文件", location: "位置", cancel: "取消", create: "创建", editor: "文件编辑器", close: "关闭", save: "保存", binaryFile: "二进制文件", binaryDetail: "此文件可以 Base64 形式读取，但不能在文本编辑器中修改。", contentAria: "文件内容",
    moveOrRename: "移动或重命名", current: "当前位置", move: "移动", destination: "目标路径", deleteEntry: "删除项目？", deleteDescription: "这会从服务器目录中移除数据，且无法撤销。", andChildren: "及其所有子项目",
    unsaved: "未保存", saved: "已保存",
  },
  en: {
    loadError: "Unable to load the file list", readFailed: "Unable to read the file", duplicate: "The name is empty or the destination already exists", directoryCreated: "Directory created", fileCreated: "File created", createFailed: "Unable to create the entry", fileSaved: "File saved", saveFailed: "Unable to save the file", entryMoved: "Entry moved", moveFailed: "Unable to move the entry", entryDeleted: "Entry deleted", deleteFailed: "Unable to delete the entry",
    newFileAria: "New file", newDirectoryAria: "New directory", refreshFilesAria: "Refresh files", newFileTitle: "New file", newDirectoryTitle: "New directory", refreshTitle: "Refresh",
    name: "Name", size: "Size", modified: "Modified", actions: "Actions", moveAria: (name: string) => `Move ${name}`, deleteAria: (name: string) => `Delete ${name}`, openAria: (name: string) => `Open ${name}`, moveTitle: "Move or rename", deleteTitle: "Delete", openTitle: "Open directory",
    unableDirectory: "Unable to load this directory.", retry: "Retry", emptyDirectory: "This directory is empty.", emptyDetail: "No files are present at this path.", entries: (count: number) => `${count} entries`, pathBoundary: "Allowed path / server-data",
    createDirectory: "Create directory", createFile: "Create file", location: "Location", cancel: "Cancel", create: "Create", editor: "File editor", close: "Close", save: "Save", binaryFile: "Binary file", binaryDetail: "This file is readable as base64 but cannot be edited in the text editor.", contentAria: "File content",
    moveOrRename: "Move or rename", current: "Current", move: "Move", destination: "Destination path", deleteEntry: "Delete entry?", deleteDescription: "This removes data from the server directory and cannot be undone.", andChildren: " and all children",
    unsaved: "Unsaved", saved: "Saved",
  },
  ja: {
    loadError: "ファイル一覧を読み込めません", readFailed: "ファイルを読み込めません", duplicate: "名前が空か、移動先がすでに存在します", directoryCreated: "ディレクトリを作成しました", fileCreated: "ファイルを作成しました", createFailed: "項目を作成できません", fileSaved: "ファイルを保存しました", saveFailed: "ファイルを保存できません", entryMoved: "項目を移動しました", moveFailed: "項目を移動できません", entryDeleted: "項目を削除しました", deleteFailed: "項目を削除できません",
    newFileAria: "新規ファイル", newDirectoryAria: "新規ディレクトリ", refreshFilesAria: "ファイルを更新", newFileTitle: "新規ファイル", newDirectoryTitle: "新規ディレクトリ", refreshTitle: "更新",
    name: "名前", size: "サイズ", modified: "更新日時", actions: "操作", moveAria: (name: string) => `${name} を移動`, deleteAria: (name: string) => `${name} を削除`, openAria: (name: string) => `${name} を開く`, moveTitle: "移動または名前変更", deleteTitle: "削除", openTitle: "ディレクトリを開く",
    unableDirectory: "このディレクトリを読み込めません。", retry: "再試行", emptyDirectory: "このディレクトリは空です。", emptyDetail: "このパスにファイルはありません。", entries: (count: number) => `${count} 件`, pathBoundary: "許可パス / server-data",
    createDirectory: "ディレクトリを作成", createFile: "ファイルを作成", location: "場所", cancel: "キャンセル", create: "作成", editor: "ファイルエディター", close: "閉じる", save: "保存", binaryFile: "バイナリファイル", binaryDetail: "このファイルは Base64 で読み取れますが、テキストエディターでは編集できません。", contentAria: "ファイル内容",
    moveOrRename: "移動または名前変更", current: "現在", move: "移動", destination: "移動先パス", deleteEntry: "項目を削除しますか？", deleteDescription: "サーバーディレクトリからデータが削除され、元に戻せません。", andChildren: " とすべての子項目",
    unsaved: "未保存", saved: "保存済み",
  },
  ko: {
    loadError: "파일 목록을 불러올 수 없습니다", readFailed: "파일을 읽을 수 없습니다", duplicate: "이름이 비어 있거나 대상이 이미 존재합니다", directoryCreated: "디렉터리를 만들었습니다", fileCreated: "파일을 만들었습니다", createFailed: "항목을 만들 수 없습니다", fileSaved: "파일을 저장했습니다", saveFailed: "파일을 저장할 수 없습니다", entryMoved: "항목을 이동했습니다", moveFailed: "항목을 이동할 수 없습니다", entryDeleted: "항목을 삭제했습니다", deleteFailed: "항목을 삭제할 수 없습니다",
    newFileAria: "새 파일", newDirectoryAria: "새 디렉터리", refreshFilesAria: "파일 새로고침", newFileTitle: "새 파일", newDirectoryTitle: "새 디렉터리", refreshTitle: "새로고침",
    name: "이름", size: "크기", modified: "수정 시간", actions: "작업", moveAria: (name: string) => `${name} 이동`, deleteAria: (name: string) => `${name} 삭제`, openAria: (name: string) => `${name} 열기`, moveTitle: "이동 또는 이름 변경", deleteTitle: "삭제", openTitle: "디렉터리 열기",
    unableDirectory: "이 디렉터리를 불러올 수 없습니다.", retry: "다시 시도", emptyDirectory: "이 디렉터리는 비어 있습니다.", emptyDetail: "이 경로에 파일이 없습니다.", entries: (count: number) => `${count}개 항목`, pathBoundary: "허용 경로 / server-data",
    createDirectory: "디렉터리 만들기", createFile: "파일 만들기", location: "위치", cancel: "취소", create: "만들기", editor: "파일 편집기", close: "닫기", save: "저장", binaryFile: "바이너리 파일", binaryDetail: "이 파일은 Base64로 읽을 수 있지만 텍스트 편집기에서 수정할 수 없습니다.", contentAria: "파일 내용",
    moveOrRename: "이동 또는 이름 변경", current: "현재", move: "이동", destination: "대상 경로", deleteEntry: "항목을 삭제할까요?", deleteDescription: "서버 디렉터리에서 데이터가 삭제되며 되돌릴 수 없습니다.", andChildren: " 및 모든 하위 항목",
    unsaved: "저장되지 않음", saved: "저장됨",
  },
});

const backupsCopy = defineCopy({
  "zh-CN": {
    loadError: "无法加载备份", downloadBackup: "下载备份", downloading: "下载中…", downloadFailed: "无法下载备份", operationUnavailable: "暂时无法获取任务状态。请先重试状态检查，再执行其他备份操作。", terminal: (status: string) => `备份任务状态：${status}`, operationName: { backup: "创建备份", restore: "恢复备份", "backup-delete": "删除备份" }, operationStatus: { queued: "等待中", leased: "已领取", dispatched: "已下发", running: "执行中", succeeded: "已完成", failed: "失败", canceled: "已取消" }, acceptedCreate: "创建备份任务已受理", created: "备份已创建", requestFailed: "无法提交备份任务", acceptedRestore: "恢复备份任务已受理", restored: "备份已恢复", restoreFailed: "无法提交恢复任务", acceptedDelete: "删除备份任务已受理", deleted: "备份已删除", deleteFailed: "无法删除备份", statusFailed: "无法查询任务状态", checksumCopied: "校验摘要已复制", checksumCopyFailed: "无法复制校验摘要",
    acceptedCleanup: "失败备份清理任务已受理", cleaned: "失败备份已清理", cleanupFailed: "无法清理失败备份",
    eyebrow: "恢复点", title: "备份", count: (count: number) => `此服务器记录了 ${count} 个快照。`, stopBeforeRestore: "恢复前请先停止服务器。", attempt: "尝试", retryStatus: "重试状态", transitionRunning: "已有备份转换操作仍在运行。", creating: (progress: number) => `创建中 · ${progress}%`, createBackup: "创建备份",
    checksumPending: "摘要待生成", copyChecksum: (name: string) => `复制 ${name} 的摘要`, status: { creating: "创建中", ready: "就绪", failed: "失败", restoring: "恢复中", deleting: "删除中" }, pending: "待处理", restoreAria: (name: string) => `恢复 ${name}`, deleteAria: (name: string) => `删除 ${name}`, stopServer: "先停止服务器", restoreBackup: "恢复备份", deleteBackup: "删除备份",
    emptyTitle: "还没有恢复点。", emptyDetail: "进行高风险更改前，请先创建手动备份。", creatingShort: "创建中…", restoreTitle: "恢复此备份？", restoreDescription: "恢复操作到达终态前，服务器必须保持停止。", cancel: "取消", restore: "恢复", checksumUnavailable: "摘要不可用", deleteTitle: "删除此备份？", deleteDescription: "删除操作完成后，此恢复点将被移除。",
    cleanupAria: (name: string) => `清理失败备份 ${name}`, cleanupFailedBackup: "清理失败备份", cleanupTitle: "清理此失败备份？", cleanupDescription: "清理操作会删除可能残留的归档和失败恢复点记录。", cleanup: "清理",
  },
  en: {
    loadError: "Unable to load backups", downloadBackup: "Download backup", downloading: "Downloading...", downloadFailed: "Unable to download backup", operationUnavailable: "Operation status is unavailable. Retry the status check before starting another backup action.", terminal: (status: string) => `Backup operation status: ${status}`, operationName: { backup: "Create backup", restore: "Restore backup", "backup-delete": "Delete backup" }, operationStatus: { queued: "Queued", leased: "Claimed", dispatched: "Dispatched", running: "Running", succeeded: "Succeeded", failed: "Failed", canceled: "Canceled" }, acceptedCreate: "Backup operation accepted", created: "Backup created", requestFailed: "Backup request failed", acceptedRestore: "Restore operation accepted", restored: "Backup restored", restoreFailed: "Restore request failed", acceptedDelete: "Backup deletion accepted", deleted: "Backup deleted", deleteFailed: "Backup deletion failed", statusFailed: "Status check failed", checksumCopied: "Checksum copied", checksumCopyFailed: "Unable to copy checksum",
    acceptedCleanup: "Failed backup cleanup accepted", cleaned: "Failed backup cleaned up", cleanupFailed: "Failed backup cleanup failed",
    eyebrow: "RECOVERY POINTS", title: "Backups", count: (count: number) => `${count} ${count === 1 ? "snapshot" : "snapshots"} recorded for this server.`, stopBeforeRestore: "Stop the server before restoring a recovery point.", attempt: "attempt", retryStatus: "Retry status", transitionRunning: "An existing backup transition is still running.", creating: (progress: number) => `Creating · ${progress}%`, createBackup: "Create backup",
    checksumPending: "Checksum pending", copyChecksum: (name: string) => `Copy checksum for ${name}`, status: { creating: "Creating", ready: "Ready", failed: "Failed", restoring: "Restoring", deleting: "Deleting" }, pending: "Pending", restoreAria: (name: string) => `Restore ${name}`, deleteAria: (name: string) => `Delete ${name}`, stopServer: "Stop the server first", restoreBackup: "Restore backup", deleteBackup: "Delete backup",
    emptyTitle: "No recovery points yet.", emptyDetail: "Create a manual backup before risky changes.", creatingShort: "Creating...", restoreTitle: "Restore this backup?", restoreDescription: "The server must remain stopped until the restore operation reaches a terminal state.", cancel: "Cancel", restore: "Restore", checksumUnavailable: "Checksum unavailable", deleteTitle: "Delete this backup?", deleteDescription: "The recovery point will be removed after the deletion operation completes.",
    cleanupAria: (name: string) => `Clean up failed backup ${name}`, cleanupFailedBackup: "Clean up failed backup", cleanupTitle: "Clean up this failed backup?", cleanupDescription: "This removes any leftover archive and the failed recovery-point record.", cleanup: "Clean up",
  },
  ja: {
    loadError: "バックアップを読み込めません", downloadBackup: "バックアップをダウンロード", downloading: "ダウンロード中…", downloadFailed: "バックアップをダウンロードできません", operationUnavailable: "操作状態を確認できません。別のバックアップ操作を始める前に、状態確認を再試行してください。", terminal: (status: string) => `バックアップ操作の状態：${status}`, operationName: { backup: "バックアップを作成", restore: "バックアップを復元", "backup-delete": "バックアップを削除" }, operationStatus: { queued: "待機中", leased: "取得済み", dispatched: "送信済み", running: "実行中", succeeded: "成功", failed: "失敗", canceled: "キャンセル済み" }, acceptedCreate: "バックアップ操作を受け付けました", created: "バックアップを作成しました", requestFailed: "バックアップ要求に失敗しました", acceptedRestore: "復元操作を受け付けました", restored: "バックアップを復元しました", restoreFailed: "復元要求に失敗しました", acceptedDelete: "バックアップ削除を受け付けました", deleted: "バックアップを削除しました", deleteFailed: "バックアップを削除できません", statusFailed: "状態を確認できません", checksumCopied: "チェックサムをコピーしました", checksumCopyFailed: "チェックサムをコピーできません",
    acceptedCleanup: "失敗したバックアップのクリーンアップを受け付けました", cleaned: "失敗したバックアップをクリーンアップしました", cleanupFailed: "失敗したバックアップをクリーンアップできません",
    eyebrow: "復元ポイント", title: "バックアップ", count: (count: number) => `このサーバーには ${count} 件のスナップショットがあります。`, stopBeforeRestore: "復元する前にサーバーを停止してください。", attempt: "試行", retryStatus: "状態を再確認", transitionRunning: "既存のバックアップ処理がまだ実行中です。", creating: (progress: number) => `作成中 · ${progress}%`, createBackup: "バックアップを作成",
    checksumPending: "チェックサム待機中", copyChecksum: (name: string) => `${name} のチェックサムをコピー`, status: { creating: "作成中", ready: "準備完了", failed: "失敗", restoring: "復元中", deleting: "削除中" }, pending: "処理待ち", restoreAria: (name: string) => `${name} を復元`, deleteAria: (name: string) => `${name} を削除`, stopServer: "先にサーバーを停止", restoreBackup: "バックアップを復元", deleteBackup: "バックアップを削除",
    emptyTitle: "復元ポイントはまだありません。", emptyDetail: "危険な変更の前に手動バックアップを作成してください。", creatingShort: "作成中…", restoreTitle: "このバックアップを復元しますか？", restoreDescription: "復元操作が完了するまでサーバーを停止したままにしてください。", cancel: "キャンセル", restore: "復元", checksumUnavailable: "チェックサムは利用できません", deleteTitle: "このバックアップを削除しますか？", deleteDescription: "削除操作が完了すると、この復元ポイントは削除されます。",
    cleanupAria: (name: string) => `${name} の失敗したバックアップをクリーンアップ`, cleanupFailedBackup: "失敗したバックアップをクリーンアップ", cleanupTitle: "この失敗したバックアップをクリーンアップしますか？", cleanupDescription: "残っている可能性のあるアーカイブと、失敗した復元ポイントの記録を削除します。", cleanup: "クリーンアップ",
  },
  ko: {
    loadError: "백업을 불러올 수 없습니다", downloadBackup: "백업 다운로드", downloading: "다운로드 중…", downloadFailed: "백업을 다운로드할 수 없습니다", operationUnavailable: "작업 상태를 확인할 수 없습니다. 다른 백업 작업을 시작하기 전에 상태 확인을 다시 시도하세요.", terminal: (status: string) => `백업 작업 상태: ${status}`, operationName: { backup: "백업 만들기", restore: "백업 복원", "backup-delete": "백업 삭제" }, operationStatus: { queued: "대기 중", leased: "할당됨", dispatched: "전송됨", running: "실행 중", succeeded: "성공", failed: "실패", canceled: "취소됨" }, acceptedCreate: "백업 작업이 접수되었습니다", created: "백업을 만들었습니다", requestFailed: "백업 요청에 실패했습니다", acceptedRestore: "복원 작업이 접수되었습니다", restored: "백업을 복원했습니다", restoreFailed: "복원 요청에 실패했습니다", acceptedDelete: "백업 삭제가 접수되었습니다", deleted: "백업을 삭제했습니다", deleteFailed: "백업을 삭제할 수 없습니다", statusFailed: "상태를 확인할 수 없습니다", checksumCopied: "체크섬을 복사했습니다", checksumCopyFailed: "체크섬을 복사할 수 없습니다",
    acceptedCleanup: "실패한 백업 정리 작업이 접수되었습니다", cleaned: "실패한 백업을 정리했습니다", cleanupFailed: "실패한 백업을 정리할 수 없습니다",
    eyebrow: "복원 지점", title: "백업", count: (count: number) => `이 서버에 ${count}개의 스냅샷이 기록되어 있습니다.`, stopBeforeRestore: "복원하기 전에 서버를 중지하세요.", attempt: "시도", retryStatus: "상태 다시 확인", transitionRunning: "기존 백업 전환 작업이 아직 실행 중입니다.", creating: (progress: number) => `생성 중 · ${progress}%`, createBackup: "백업 만들기",
    checksumPending: "체크섬 대기 중", copyChecksum: (name: string) => `${name} 체크섬 복사`, status: { creating: "생성 중", ready: "준비됨", failed: "실패", restoring: "복원 중", deleting: "삭제 중" }, pending: "대기 중", restoreAria: (name: string) => `${name} 복원`, deleteAria: (name: string) => `${name} 삭제`, stopServer: "먼저 서버 중지", restoreBackup: "백업 복원", deleteBackup: "백업 삭제",
    emptyTitle: "아직 복원 지점이 없습니다.", emptyDetail: "위험한 변경 전에 수동 백업을 만드세요.", creatingShort: "생성 중…", restoreTitle: "이 백업을 복원할까요?", restoreDescription: "복원 작업이 완료될 때까지 서버를 중지 상태로 유지해야 합니다.", cancel: "취소", restore: "복원", checksumUnavailable: "체크섬을 사용할 수 없습니다", deleteTitle: "이 백업을 삭제할까요?", deleteDescription: "삭제 작업이 완료되면 이 복원 지점이 제거됩니다.",
    cleanupAria: (name: string) => `${name} 실패한 백업 정리`, cleanupFailedBackup: "실패한 백업 정리", cleanupTitle: "이 실패한 백업을 정리할까요?", cleanupDescription: "남아 있을 수 있는 아카이브와 실패한 복원 지점 기록을 삭제합니다.", cleanup: "정리",
  },
});

const networkCopy = defineCopy({
  "zh-CN": {
    loadError: "无法加载网络端点", reconcileFallback: "网络配置同步失败", configChanged: "服务器配置已变更。已重新加载最新配置版本，请检查后重试。", updateFailed: "网络更新失败", invalidEndpoint: "绑定 IP 和端口必须组成有效地址。", allocationAdded: "已添加端点", primaryChanged: "主端点已变更", allocationReleased: "已释放端点",
    eyebrow: "端点管理", title: "网络端点", active: (count: number, generation: number) => `${count} 个活动端点 · 配置版本 ${generation}`, add: "添加端点", reconciling: "正在同步网络配置", reload: "重新加载", endpoint: "端点", protocol: "协议", role: "用途", updated: "更新时间", actions: "操作", primary: "主端点", secondary: "备用端点", setPrimary: (endpoint: string) => `将 ${endpoint} 设为主端点`, deleteAria: (endpoint: string) => `删除 ${endpoint}`, setPrimaryTitle: "设为主端点", primaryLocked: "主端点不能删除", deleteTitle: "删除端点", none: "没有网络端点。", loading: "正在加载网络端点",
    addTitle: "添加网络端点", nodeGeneration: (node: string, generation: number) => `节点：${node} / 基准版本 ${generation}`, cancel: "取消", addAllocation: "添加端点", bindIp: "绑定 IP", port: "端口", tcp: "TCP", udp: "UDP", makePrimary: "设为主端点", makePrimaryHint: "使用此端点作为服务器连接地址。", releaseTitle: "释放端点？", releaseDescription: "此端点将不再分配给该服务器。", release: "释放", attempt: "尝试", generation: "配置版本",
  },
  en: {
    loadError: "Unable to load allocations", reconcileFallback: "Network configuration sync failed", configChanged: "Configuration changed on the server. The latest configuration version was reloaded; review and retry.", updateFailed: "Network update failed", invalidEndpoint: "Bind IP and port must identify a valid endpoint.", allocationAdded: "Allocation added", primaryChanged: "Primary allocation changed", allocationReleased: "Allocation released",
    eyebrow: "ENDPOINT CONTROL", title: "Network allocations", active: (count: number, generation: number) => `${count} active ${count === 1 ? "endpoint" : "endpoints"} · configuration version ${generation}`, add: "Add allocation", reconciling: "Syncing network configuration", reload: "Reload", endpoint: "Endpoint", protocol: "Protocol", role: "Role", updated: "Updated", actions: "Actions", primary: "Primary", secondary: "Secondary", setPrimary: (endpoint: string) => `Set ${endpoint} as primary`, deleteAria: (endpoint: string) => `Delete ${endpoint}`, setPrimaryTitle: "Set primary", primaryLocked: "Primary allocation cannot be deleted", deleteTitle: "Delete allocation", none: "No allocations.", loading: "Loading network allocations",
    addTitle: "Add network allocation", nodeGeneration: (node: string, generation: number) => `Node: ${node} / base version ${generation}`, cancel: "Cancel", addAllocation: "Add allocation", bindIp: "Bind IP", port: "Port", tcp: "TCP", udp: "UDP", makePrimary: "Make primary", makePrimaryHint: "Use this endpoint in the server connection address.", releaseTitle: "Release allocation?", releaseDescription: "The endpoint will no longer be assigned to this server.", release: "Release", attempt: "attempt", generation: "configuration version",
  },
  ja: {
    loadError: "ネットワーク割り当てを読み込めません", reconcileFallback: "ネットワーク設定の同期に失敗しました", configChanged: "サーバー上の設定が変更されました。最新の設定バージョンを読み込みました。確認して再試行してください。", updateFailed: "ネットワークを更新できません", invalidEndpoint: "バインド IP とポートで有効なエンドポイントを指定してください。", allocationAdded: "割り当てを追加しました", primaryChanged: "プライマリ割り当てを変更しました", allocationReleased: "割り当てを解放しました",
    eyebrow: "エンドポイント管理", title: "ネットワーク割り当て", active: (count: number, generation: number) => `${count} 個のアクティブなエンドポイント · 設定バージョン ${generation}`, add: "割り当てを追加", reconciling: "ネットワーク設定を同期中", reload: "再読み込み", endpoint: "エンドポイント", protocol: "プロトコル", role: "役割", updated: "更新日時", actions: "操作", primary: "プライマリ", secondary: "セカンダリ", setPrimary: (endpoint: string) => `${endpoint} をプライマリに設定`, deleteAria: (endpoint: string) => `${endpoint} を削除`, setPrimaryTitle: "プライマリに設定", primaryLocked: "プライマリ割り当ては削除できません", deleteTitle: "割り当てを削除", none: "割り当てはありません。", loading: "ネットワーク割り当てを読み込み中",
    addTitle: "ネットワーク割り当てを追加", nodeGeneration: (node: string, generation: number) => `ノード：${node} / 基準バージョン ${generation}`, cancel: "キャンセル", addAllocation: "割り当てを追加", bindIp: "バインド IP", port: "ポート", tcp: "TCP", udp: "UDP", makePrimary: "プライマリにする", makePrimaryHint: "このエンドポイントをサーバー接続アドレスに使用します。", releaseTitle: "割り当てを解放しますか？", releaseDescription: "このエンドポイントはサーバーから割り当て解除されます。", release: "解放", attempt: "試行", generation: "設定バージョン",
  },
  ko: {
    loadError: "네트워크 할당을 불러올 수 없습니다", reconcileFallback: "네트워크 설정 동기화에 실패했습니다", configChanged: "서버의 설정이 변경되었습니다. 최신 설정 버전을 다시 불러왔습니다. 확인 후 다시 시도하세요.", updateFailed: "네트워크를 업데이트할 수 없습니다", invalidEndpoint: "바인드 IP와 포트가 유효한 엔드포인트를 지정해야 합니다.", allocationAdded: "할당을 추가했습니다", primaryChanged: "기본 할당을 변경했습니다", allocationReleased: "할당을 해제했습니다",
    eyebrow: "엔드포인트 관리", title: "네트워크 할당", active: (count: number, generation: number) => `활성 엔드포인트 ${count}개 · 설정 버전 ${generation}`, add: "할당 추가", reconciling: "네트워크 설정 동기화 중", reload: "다시 불러오기", endpoint: "엔드포인트", protocol: "프로토콜", role: "역할", updated: "업데이트", actions: "작업", primary: "기본", secondary: "보조", setPrimary: (endpoint: string) => `기본 엔드포인트로 설정: ${endpoint}`, deleteAria: (endpoint: string) => `엔드포인트 삭제: ${endpoint}`, setPrimaryTitle: "기본으로 설정", primaryLocked: "기본 할당은 삭제할 수 없습니다", deleteTitle: "할당 삭제", none: "할당이 없습니다.", loading: "네트워크 할당을 불러오는 중",
    addTitle: "네트워크 할당 추가", nodeGeneration: (node: string, generation: number) => `노드: ${node} / 기준 버전 ${generation}`, cancel: "취소", addAllocation: "할당 추가", bindIp: "바인드 IP", port: "포트", tcp: "TCP", udp: "UDP", makePrimary: "기본으로 설정", makePrimaryHint: "이 엔드포인트를 서버 연결 주소로 사용합니다.", releaseTitle: "할당을 해제할까요?", releaseDescription: "이 엔드포인트는 더 이상 이 서버에 할당되지 않습니다.", release: "해제", attempt: "시도", generation: "설정 버전",
  },
});

const startupCopy = defineCopy({
  "zh-CN": {
    loadError: "无法加载启动配置", integerRange: (key: string) => `${key} 超出允许的整数范围。`, integerHint: (minimum: string, maximum: string) => `${minimum} 至 ${maximum}`, schema: (key: string) => `${key} 不符合该变量的配置规则。`, reconcileFallback: (status: string) => `启动配置同步状态：${status}`, saved: "启动配置已保存", changed: "服务器配置已变更。已重新加载最新配置版本，请检查后重试。", updateFailed: "启动配置更新失败", loading: "正在加载启动配置", unavailable: "启动配置不可用", eyebrow: "运行时入口", title: "启动配置", declared: (count: number, generation: number) => `${count} 个变量 · 配置版本 ${generation}`, save: "保存启动配置", reconciling: "正在同步启动配置", reload: "重新加载", resolved: "解析后的命令", process: "进程命令", executable: "可执行文件", arguments: "参数", definition: "模板版本", bundle: "运行包", declaredSchema: "变量规则", variables: "环境变量", pending: "项待处理", required: "必填", secretConfigured: "密钥已配置", secretMissing: "密钥未配置", requiredHint: "必填", optionalHint: "可选", configuredReplacement: "已配置，输入新值即可替换", enterSecret: "输入密钥", willClear: "将被清除", configured: "已配置", notConfigured: "未配置", usingDefault: "使用默认值", clearAgain: "撤销清除", clear: "清除", attempt: "尝试", generation: "配置版本",
  },
  en: {
    loadError: "Unable to load startup configuration", integerRange: (key: string) => `${key} is outside its allowed integer range.`, integerHint: (minimum: string, maximum: string) => `${minimum} to ${maximum}`, schema: (key: string) => `${key} does not satisfy its declared schema.`, reconcileFallback: (status: string) => `Startup configuration sync: ${status}`, saved: "Startup configuration saved", changed: "Configuration changed on the server. The latest configuration version was reloaded; review and retry.", updateFailed: "Startup update failed", loading: "Loading startup configuration", unavailable: "Startup configuration is unavailable", eyebrow: "RUNTIME ENTRYPOINT", title: "Startup configuration", declared: (count: number, generation: number) => `${count} declared variables · configuration version ${generation}`, save: "Save startup", reconciling: "Syncing startup configuration", reload: "Reload", resolved: "RESOLVED COMMAND", process: "Process command", executable: "Executable", arguments: "Arguments", definition: "Template version", bundle: "Runtime package", declaredSchema: "VARIABLE RULES", variables: "Environment variables", pending: "pending", required: "required", secretConfigured: "Secret configured", secretMissing: "Secret not configured", requiredHint: "Required", optionalHint: "Optional", configuredReplacement: "Configured. Enter a new value to replace it", enterSecret: "Enter secret", willClear: "Will be cleared", configured: "Configured", notConfigured: "Not configured", usingDefault: "Using default", clearAgain: "Undo clear", clear: "Clear", attempt: "attempt", generation: "configuration version",
  },
  ja: {
    loadError: "起動設定を読み込めません", integerRange: (key: string) => `${key} は許可された整数範囲外です。`, integerHint: (minimum: string, maximum: string) => `${minimum} ～ ${maximum}`, schema: (key: string) => `${key} は定義された条件を満たしていません。`, reconcileFallback: (status: string) => `起動設定の同期状態：${status}`, saved: "起動設定を保存しました", changed: "サーバー上の設定が変更されました。最新の設定バージョンを読み込みました。確認して再試行してください。", updateFailed: "起動設定を更新できません", loading: "起動設定を読み込み中", unavailable: "起動設定を利用できません", eyebrow: "ランタイムエントリポイント", title: "起動設定", declared: (count: number, generation: number) => `${count} 個の変数 · 設定バージョン ${generation}`, save: "起動設定を保存", reconciling: "起動設定を同期中", reload: "再読み込み", resolved: "解決済みコマンド", process: "プロセスコマンド", executable: "実行ファイル", arguments: "引数", definition: "テンプレートバージョン", bundle: "実行パッケージ", declaredSchema: "変数ルール", variables: "環境変数", pending: "件を変更", required: "必須", secretConfigured: "シークレット設定済み", secretMissing: "シークレット未設定", requiredHint: "必須", optionalHint: "任意", configuredReplacement: "設定済みです。新しい値を入力すると置き換わります", enterSecret: "シークレットを入力", willClear: "消去予定", configured: "設定済み", notConfigured: "未設定", usingDefault: "デフォルト値を使用", clearAgain: "消去を取り消す", clear: "消去", attempt: "試行", generation: "設定バージョン",
  },
  ko: {
    loadError: "시작 설정을 불러올 수 없습니다", integerRange: (key: string) => `${key} 항목이 허용된 정수 범위를 벗어났습니다.`, integerHint: (minimum: string, maximum: string) => `${minimum} ~ ${maximum}`, schema: (key: string) => `${key} 항목이 정의된 형식에 맞지 않습니다.`, reconcileFallback: (status: string) => `시작 설정 동기화 상태: ${status}`, saved: "시작 설정을 저장했습니다", changed: "서버의 설정이 변경되었습니다. 최신 설정 버전을 다시 불러왔습니다. 확인 후 다시 시도하세요.", updateFailed: "시작 설정을 업데이트할 수 없습니다", loading: "시작 설정을 불러오는 중", unavailable: "시작 설정을 사용할 수 없습니다", eyebrow: "런타임 진입점", title: "시작 설정", declared: (count: number, generation: number) => `변수 ${count}개 · 설정 버전 ${generation}`, save: "시작 설정 저장", reconciling: "시작 설정 동기화 중", reload: "다시 불러오기", resolved: "확정된 명령", process: "프로세스 명령", executable: "실행 파일", arguments: "인수", definition: "템플릿 버전", bundle: "실행 패키지", declaredSchema: "변수 규칙", variables: "환경 변수", pending: "개 변경됨", required: "필수", secretConfigured: "시크릿 설정됨", secretMissing: "시크릿 미설정", requiredHint: "필수", optionalHint: "선택 사항", configuredReplacement: "설정됨. 새 값을 입력하면 교체됩니다", enterSecret: "시크릿 입력", willClear: "삭제 예정", configured: "설정됨", notConfigured: "설정되지 않음", usingDefault: "기본값 사용", clearAgain: "삭제 취소", clear: "지우기", attempt: "시도", generation: "설정 버전",
  },
});

const activityCopy = defineCopy({
  "zh-CN": {
    loadError: "无法加载服务器任务。", eyebrow: "服务器记录", title: "任务记录", refresh: "刷新任务记录", loading: "正在加载任务记录", retry: "重试", emptyTitle: "这台服务器还没有任务记录。", emptyDetail: "启停、备份和配置同步等任务会显示在这里。", status: { queued: "等待中", leased: "已领取", dispatched: "已下发", running: "执行中", succeeded: "已完成", failed: "失败", canceled: "已取消" }, operationType: { provision: "创建服务器", start: "启动服务器", stop: "停止服务器", restart: "重启服务器", kill: "强制终止服务器", backup: "创建备份", restore: "恢复备份", "backup-delete": "删除备份", delete: "删除服务器", reconcile: "同步服务器状态" },
  },
  en: {
    loadError: "Unable to load server tasks.", eyebrow: "SERVER HISTORY", title: "Task history", refresh: "Refresh task history", loading: "Loading task history", retry: "Retry", emptyTitle: "No task history yet.", emptyDetail: "Power, backup, and configuration sync tasks will appear here.", status: { queued: "Queued", leased: "Claimed", dispatched: "Dispatched", running: "Running", succeeded: "Succeeded", failed: "Failed", canceled: "Canceled" }, operationType: { provision: "Create server", start: "Start server", stop: "Stop server", restart: "Restart server", kill: "Force terminate server", backup: "Create backup", restore: "Restore backup", "backup-delete": "Delete backup", delete: "Delete server", reconcile: "Sync server state" },
  },
  ja: {
    loadError: "サーバーのタスク履歴を読み込めません。", eyebrow: "サーバー履歴", title: "タスク履歴", refresh: "タスク履歴を更新", loading: "タスク履歴を読み込み中", retry: "再試行", emptyTitle: "タスク履歴はまだありません。", emptyDetail: "電源操作、バックアップ、設定同期のタスクがここに表示されます。", status: { queued: "待機中", leased: "取得済み", dispatched: "送信済み", running: "実行中", succeeded: "成功", failed: "失敗", canceled: "キャンセル済み" }, operationType: { provision: "サーバーを作成", start: "サーバーを起動", stop: "サーバーを停止", restart: "サーバーを再起動", kill: "サーバーを強制終了", backup: "バックアップを作成", restore: "バックアップを復元", "backup-delete": "バックアップを削除", delete: "サーバーを削除", reconcile: "サーバー状態を同期" },
  },
  ko: {
    loadError: "서버 작업 기록을 불러올 수 없습니다.", eyebrow: "서버 기록", title: "작업 기록", refresh: "작업 기록 새로 고침", loading: "작업 기록을 불러오는 중", retry: "다시 시도", emptyTitle: "아직 작업 기록이 없습니다.", emptyDetail: "전원, 백업, 설정 동기화 작업이 여기에 표시됩니다.", status: { queued: "대기 중", leased: "할당됨", dispatched: "전송됨", running: "실행 중", succeeded: "성공", failed: "실패", canceled: "취소됨" }, operationType: { provision: "서버 생성", start: "서버 시작", stop: "서버 중지", restart: "서버 재시작", kill: "서버 강제 종료", backup: "백업 생성", restore: "백업 복원", "backup-delete": "백업 삭제", delete: "서버 삭제", reconcile: "서버 상태 동기화" },
  },
});

const settingsCopy = defineCopy({
  "zh-CN": { identity: "基本信息", details: "服务器详情", serverId: "服务器 ID", owner: "所有者", gameVersion: "游戏版本", definition: "模板版本", bundle: "运行包摘要", allocation: "连接地址", desiredState: "目标状态", reconciliation: "状态同步", lifecycle: "生命周期", desiredPower: "目标运行状态", observedPower: "当前运行状态", generation: "配置版本", observedGeneration: "已生效版本", lifecycleState: { provisioning: "创建中", ready: "就绪", deleting: "删除中", deleted: "已删除" }, powerStatus: { unknown: "未知", stopped: "已停止", starting: "启动中", running: "运行中", stopping: "停止中" } },
  en: { identity: "BASIC INFORMATION", details: "Server details", serverId: "Server ID", owner: "Owner", gameVersion: "Game version", definition: "Template version", bundle: "Runtime bundle digest", allocation: "Connection address", desiredState: "TARGET STATE", reconciliation: "State sync", lifecycle: "Lifecycle", desiredPower: "Target power state", observedPower: "Current power state", generation: "Configuration version", observedGeneration: "Applied version", lifecycleState: { provisioning: "Creating", ready: "Ready", deleting: "Deleting", deleted: "Deleted" }, powerStatus: { unknown: "Unknown", stopped: "Stopped", starting: "Starting", running: "Running", stopping: "Stopping" } },
  ja: { identity: "基本情報", details: "サーバー詳細", serverId: "サーバー ID", owner: "所有者", gameVersion: "ゲームバージョン", definition: "テンプレートバージョン", bundle: "実行パッケージのダイジェスト", allocation: "接続先", desiredState: "目標状態", reconciliation: "状態同期", lifecycle: "ライフサイクル", desiredPower: "目標稼働状態", observedPower: "現在の稼働状態", generation: "設定バージョン", observedGeneration: "適用済みバージョン", lifecycleState: { provisioning: "作成中", ready: "準備完了", deleting: "削除中", deleted: "削除済み" }, powerStatus: { unknown: "不明", stopped: "停止済み", starting: "起動中", running: "稼働中", stopping: "停止中" } },
  ko: { identity: "기본 정보", details: "서버 세부 정보", serverId: "서버 ID", owner: "소유자", gameVersion: "게임 버전", definition: "템플릿 버전", bundle: "실행 패키지 다이제스트", allocation: "접속 주소", desiredState: "목표 상태", reconciliation: "상태 동기화", lifecycle: "수명 주기", desiredPower: "목표 실행 상태", observedPower: "현재 실행 상태", generation: "설정 버전", observedGeneration: "적용된 버전", lifecycleState: { provisioning: "생성 중", ready: "준비 완료", deleting: "삭제 중", deleted: "삭제됨" }, powerStatus: { unknown: "알 수 없음", stopped: "중지됨", starting: "시작 중", running: "실행 중", stopping: "중지 중" } },
});

export function ServerWorkspace() {
  const copy = useCopy(shellCopy);
  const { serverId = "" } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const { session, toast } = useAppContext();
  const [server, setServer] = useState<Server | null>(null);
  const [permissions, setPermissions] = useState<ServerPermission[] | null>(null);
  const [error, setError] = useState("");
  const [busyAction, setBusyAction] = useState("");
  const [killOpen, setKillOpen] = useState(false);
  const tab = (location.pathname.split("/")[3] || "overview") as Tab;
  const hasPermission = (permission: ServerPermission): boolean => permissions?.includes(permission) ?? false;
  const load = useCallback(async (): Promise<void> => {
    try {
      const [nextServer, access] = await Promise.all([
        api.server(serverId),
        api.serverPermissions(serverId),
      ]);
      setServer(nextServer);
      setPermissions(access.permissions);
      setError("");
    } catch (reason) {
      setServer(null);
      setPermissions(null);
      setError(reason instanceof Error ? reason.message : copy.loadError);
    }
  }, [copy.loadError, serverId]);
  useEffect(() => { load(); const timer = window.setInterval(load, 4000); return () => window.clearInterval(timer); }, [load]);
  const power = async (action: PowerAction) => {
    if (!hasPermission("servers.power")) return;
    setBusyAction(action);
    try {
      const operation = await api.power(serverId, action, session.csrfToken);
      const actionLabel = copy.powerAction[action];
      toast(copy.powerAccepted(actionLabel), "warning");
      setKillOpen(false);
      setServer((current) => current ? { ...current, observedPower: action === "start" ? "starting" : "stopping", desiredPower: action === "stop" || action === "kill" ? "stopped" : current.desiredPower } : current);
      const completed = await pollOperation(operation, api.operation);
      await load();
      if (completed.status === "succeeded") toast(copy.powerCompleted(actionLabel), "success");
      else toast(operationFailureMessage(completed, copy.powerTerminal(actionLabel, copy.operationStatus[completed.status])), "danger");
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : copy.powerRequestFailed, "danger");
    } finally { setBusyAction(""); }
  };
  if (error && !server) return <section className="page"><ErrorState message={error} onRetry={load} /></section>;
  if (!server || !permissions) return <section className="page"><LoadingState label={copy.loading} /></section>;
  const requiredPermission: Record<Tab, ServerPermission> = {
    overview: "servers.read",
    console: "servers.console",
    files: "servers.files.read",
    backups: "servers.backups.read",
    network: "servers.network.read",
    startup: "servers.startup.read",
    activity: "servers.read",
    settings: "servers.read",
  };
  if (!requiredPermission[tab] || !hasPermission(requiredPermission[tab])) {
    return <section className="page"><div className="page-state page-state-error" role="alert"><CircleAlert size={24} /><div><strong>{copy.permissionDenied}</strong><span>{copy.permissionDenied}</span><Link className="button secondary" to={`/servers/${server.id}`}>{copy.tabs.overview}</Link></div></div></section>;
  }
  const powerLocked = isPowerControlLocked(server.observedPower, busyAction, server.nodeCondition);
  const tabs: { value: Tab; label: string; icon: React.ReactNode; permission: ServerPermission }[] = [
    { value: "overview", label: copy.tabs.overview, icon: <Gauge size={15} />, permission: "servers.read" },
    { value: "console", label: copy.tabs.console, icon: <TerminalSquare size={15} />, permission: "servers.console" },
    { value: "files", label: copy.tabs.files, icon: <Folder size={15} />, permission: "servers.files.read" },
    { value: "backups", label: copy.tabs.backups, icon: <Archive size={15} />, permission: "servers.backups.read" },
    { value: "network", label: copy.tabs.network, icon: <Network size={15} />, permission: "servers.network.read" },
    { value: "startup", label: copy.tabs.startup, icon: <Braces size={15} />, permission: "servers.startup.read" },
    { value: "activity", label: copy.tabs.activity, icon: <Activity size={15} />, permission: "servers.read" },
    { value: "settings", label: copy.tabs.settings, icon: <Settings2 size={15} />, permission: "servers.read" },
  ].filter((item) => hasPermission(item.permission as ServerPermission)) as { value: Tab; label: string; icon: React.ReactNode; permission: ServerPermission }[];
  const tabPath = (value: Tab) => value === "overview" ? `/servers/${server.id}` : `/servers/${server.id}/${value}`;
  const onTabKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex = index;
    if (event.key === "ArrowRight") nextIndex = (index + 1) % tabs.length;
    else if (event.key === "ArrowLeft") nextIndex = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = tabs.length - 1;
    else return;
    event.preventDefault();
    event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[nextIndex]?.focus();
    navigate(tabPath(tabs[nextIndex].value));
  };
  return <section className="page workspace-page"><div className="workspace-back"><Link to="/servers"><ArrowLeft size={15} />{copy.allServers}</Link><span>/</span><span translate="no">{server.gameName}</span></div><div className="server-hero"><div className="server-hero-main"><div className={`game-avatar game-avatar-large game-${server.gameName.toLowerCase().replace(/[^a-z]/g, "")}`}>{server.gameName.slice(0, 1)}</div><div><div className="server-hero-title"><h1 translate="no">{server.name}</h1><StatusBadge tone={toneForPower(server.observedPower)} pulse={["starting", "stopping"].includes(server.observedPower)}>{copy.powerStatus[server.observedPower]}</StatusBadge>{server.nodeCondition !== "available" && <StatusBadge tone={toneForNode(server.nodeCondition)}>{nodeLabel[server.nodeCondition]}</StatusBadge>}{server.healthCondition === "unhealthy" && <StatusBadge tone="danger">{powerLabel.unhealthy}</StatusBadge>}</div><p><span translate="no">{server.gameName} {server.gameVersion}</span> <i /> <span translate="no">{server.nodeName}</span> <i /> <code translate="no">{server.allocation}</code></p></div></div><div className="power-controls"><button className="icon-button" onClick={load} title={copy.refreshServer} aria-label={copy.refreshServer}><RefreshCw size={17} /></button>{hasPermission("servers.power") && <>{server.observedPower === "stopped" || server.observedPower === "unknown" ? <button className="button power-start" onClick={() => power("start")} disabled={powerLocked}><Play size={16} fill="currentColor" />{copy.start}</button> : <><button className="button power-restart" onClick={() => power("restart")} disabled={powerLocked}><RotateCcw size={16} />{copy.restart}</button><button className="button power-stop" onClick={() => power("stop")} disabled={powerLocked}><Square size={15} fill="currentColor" />{copy.stop}</button></>}<button className="icon-button danger-button" onClick={() => setKillOpen(true)} title={copy.forceTerminate} aria-label={copy.forceTerminate} disabled={powerLocked}><Power size={17} /></button></>}</div></div><nav className="workspace-tabs" aria-label={copy.serverPages} role="tablist">{tabs.map((item, index) => <button key={item.value} id={`workspace-tab-${item.value}`} className={tab === item.value ? "active" : ""} onClick={() => navigate(tabPath(item.value))} onKeyDown={(event) => onTabKeyDown(event, index)} role="tab" aria-selected={tab === item.value} aria-controls="workspace-tabpanel" tabIndex={tab === item.value ? 0 : -1}>{item.icon}{item.label}</button>)}</nav><div className="workspace-mobile-nav"><label htmlFor="workspace-page-select">{copy.serverPages}</label><select id="workspace-page-select" value={tab} onChange={(event) => navigate(tabPath(event.currentTarget.value as Tab))} aria-controls="workspace-tabpanel">{tabs.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></div><div id="workspace-tabpanel" className="workspace-content" role="tabpanel" aria-labelledby={`workspace-tab-${tab}`}>{tab === "overview" && <ServerOverview server={server} />}{tab === "console" && <ConsoleTab server={server} />}{tab === "files" && <FilesTab server={server} canWrite={hasPermission("servers.files.write")} />}{tab === "backups" && <BackupsTab server={server} canCreate={hasPermission("servers.backups.create")} canRestore={hasPermission("servers.backups.restore")} canDelete={hasPermission("servers.backups.delete")} />}{tab === "network" && <NetworkTab server={server} refreshServer={load} canWrite={hasPermission("servers.network.write")} />}{tab === "startup" && <StartupTab server={server} refreshServer={load} canWrite={hasPermission("servers.startup.write")} />}{tab === "activity" && <ActivityTab server={server} />}{tab === "settings" && <SettingsTab server={server} />}</div><Modal open={killOpen && hasPermission("servers.power")} title={copy.terminateTitle} description={copy.terminateDescription} onClose={() => setKillOpen(false)} footer={<><button className="button secondary" onClick={() => setKillOpen(false)}>{copy.cancel}</button><button className="button danger-solid" onClick={() => power("kill")} disabled={powerLocked}><Power size={16} />{copy.forceTerminate}</button></>}><div className="danger-confirm"><CircleAlert size={22} /><div><strong translate="no">{server.name}</strong><span>{server.allocation} / {copy.generation} {server.generation}</span></div></div></Modal></section>;
}

function ServerOverview({ server }: { server: Server }) {
  const copy = useCopy(overviewCopy);
  const { locale } = useI18n();
  const cpuValues = server.metricHistory?.map((point) => point.cpuPercent) ?? [];
  const memoryValues = server.metricHistory?.map((point) => point.memoryBytes / 1024 / 1024) ?? [];
  const memoryPercent = server.metrics.memoryLimitBytes ? (server.metrics.memoryBytes / server.metrics.memoryLimitBytes) * 100 : 0;
  const diskPercent = server.metrics.diskLimitBytes ? (server.metrics.diskBytes / server.metrics.diskLimitBytes) * 100 : 0;
  const playersOnline = server.metrics.playersOnline;
  const playersMax = server.metrics.playersMax;
  const hasPlayerTelemetry = playersOnline !== undefined && playersMax !== undefined;
  const playerPercent = hasPlayerTelemetry && playersMax > 0 ? Math.min(100, (playersOnline / playersMax) * 100) : 0;
  return <><div className="workspace-metric-grid"><div className="workspace-metric"><div className="workspace-metric-head"><span><Gauge size={15} />{copy.cpuLoad}</span><strong>{Math.round(server.metrics.cpuPercent)}%</strong></div><MetricBars values={cpuValues} tone="mint" label={copy.cpuTrend} /><small>{copy.lastHour}</small></div><div className="workspace-metric"><div className="workspace-metric-head"><span><MemoryStick size={15} />{copy.memory}</span><strong>{formatBytes(server.metrics.memoryBytes)}</strong></div><MetricBars values={memoryValues} tone="blue" label={copy.memoryTrend} /><small>{Math.round(memoryPercent)}% · {copy.limit(formatBytes(server.metrics.memoryLimitBytes))}</small></div><div className="workspace-metric"><div className="workspace-metric-head"><span><Users size={15} />{copy.players}</span>{hasPlayerTelemetry ? <strong>{playersOnline}<em> / {playersMax}</em></strong> : <strong className="metric-unknown">—</strong>}</div>{hasPlayerTelemetry ? <div className="player-capacity" aria-hidden="true"><div className="player-capacity-main"><Users size={17} /><span><i style={{ width: `${playerPercent}%` }} /></span><b>{Math.round(playerPercent)}%</b></div><div className="player-capacity-scale"><span>0</span><span>{playersMax}</span></div></div> : <div className="player-capacity player-capacity-empty"><Users size={17} /><span>{copy.playersUnavailable}</span></div>}<small>{copy.developmentSnapshot}</small></div></div><div className="split-grid workspace-overview-grid"><div className="panel resource-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.resourceEyebrow}</p><h2>{copy.runtimeLimits}</h2></div><ShieldCheck size={19} /></div><ResourceLine icon={<Gauge size={16} />} label={copy.cpu} used={`${Math.round(server.metrics.cpuPercent)}%`} detail={copy.ofNodeShares} percent={server.metrics.cpuPercent} /><ResourceLine icon={<MemoryStick size={16} />} label={copy.memory} used={formatBytes(server.metrics.memoryBytes)} detail={copy.limit(formatBytes(server.metrics.memoryLimitBytes))} percent={memoryPercent} blue /><ResourceLine icon={<HardDrive size={16} />} label={copy.disk} used={formatBytes(server.metrics.diskBytes)} detail={copy.limit(formatBytes(server.metrics.diskLimitBytes))} percent={diskPercent} blue /></div><div className="panel connection-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.connectionEyebrow}</p><h2>{copy.allocation}</h2></div><Network size={19} /></div><div className="allocation-box"><code translate="no">{server.allocation}</code><button className="icon-button" onClick={() => navigator.clipboard.writeText(server.allocation)} aria-label={copy.copyAddress} title={copy.copyAddress}><Clipboard size={15} /></button></div><dl className="detail-list compact-list"><div><dt>{copy.node}</dt><dd translate="no">{server.nodeName}</dd></div><div><dt>{copy.game}</dt><dd translate="no">{server.gameName} {server.gameVersion}</dd></div><div><dt>{copy.definition}</dt><dd translate="no">{server.gameId}@{server.gameDefinitionVersion}</dd></div><div><dt>{copy.owner}</dt><dd translate="no">{server.ownerName}</dd></div><div><dt>{copy.observedGeneration}</dt><dd>{server.observedGeneration} / {server.generation}</dd></div><div><dt>{copy.lastUpdate}</dt><dd>{localizedRelativeTime(server.updatedAt, locale)}</dd></div></dl></div></div></>;
}

function ResourceLine({ icon, label, used, detail, percent, blue = false }: { icon: React.ReactNode; label: string; used: string; detail: string; percent: number; blue?: boolean }) { return <div className="resource-line"><div className="resource-line-top"><span>{icon}{label}</span><strong>{used} <small>{detail}</small></strong></div><div className={`resource-track${blue ? " track-blue" : ""}`}><i style={{ width: `${Math.min(100, percent)}%` }} /></div></div>; }

interface ReceivedConsoleLine extends ConsoleLine {
  receivedAt: string;
}

function ConsoleTab({ server }: { server: Server }) {
  const copy = useCopy(consoleCopy);
  const { session, toast } = useAppContext();
  const [lines, setLines] = useState<ReceivedConsoleLine[]>([]);
  const [command, setCommand] = useState("");
  const [sending, setSending] = useState(false);
  const [autoScroll, setAutoScroll] = useState(true);
  const [history, setHistory] = useState<string[]>([]);
  const [historyIndex, setHistoryIndex] = useState(-1);
  const consoleRef = useRef<HTMLDivElement>(null);
  const draftRef = useRef("");

  const load = useCallback(() => api.console(server.id).then((next) => {
    const receivedAt = new Date().toISOString();
    setLines(next.map((line) => ({ ...line, receivedAt })));
  }).catch(() => undefined), [server.id]);

  useEffect(() => {
    load();
    // 轮询作为 WebSocket 不可用时的兜底：WS 建立后停表，断开后恢复。
    let timer: number | undefined;
    const startPolling = () => { if (timer === undefined) timer = window.setInterval(load, 1800); };
    const stopPolling = () => { if (timer !== undefined) { window.clearInterval(timer); timer = undefined; } };
    startPolling();

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(`${proto}//${window.location.host}${api.consoleStreamPath(server.id)}`);
    socket.onopen = () => stopPolling();
    socket.onmessage = (event) => {
      let frame: { type?: string; lines?: ConsoleLine[]; line?: ConsoleLine };
      try {
        frame = JSON.parse(String(event.data));
      } catch {
        return;
      }
      if (frame.type === "history" && Array.isArray(frame.lines)) {
        const receivedAt = new Date().toISOString();
        setLines(frame.lines.map((line) => ({ ...line, receivedAt })));
      } else if (frame.type === "line" && frame.line) {
        const receivedAt = new Date().toISOString();
        const { sequence, timestamp, stream, message } = frame.line;
        setLines((prev) => [...prev.slice(-499), { sequence, timestamp, stream, message, receivedAt }]);
      }
    };
    const fallbackToPolling = () => startPolling();
    socket.onclose = fallbackToPolling;
    socket.onerror = fallbackToPolling;

    return () => {
      stopPolling();
      socket.close();
    };
  }, [load, server.id]);

  useEffect(() => {
    if (autoScroll) {
      const el = consoleRef.current;
      if (el) el.scrollTop = el.scrollHeight;
    }
  }, [lines, autoScroll]);

  const handleScroll = useCallback(() => {
    const el = consoleRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setAutoScroll(atBottom);
  }, []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const trimmed = command.trim();
    if (!trimmed) return;
    setSending(true);
    try {
      await api.command(server.id, trimmed, session.csrfToken);
      setHistory((prev) => {
        if (prev.length > 0 && prev[prev.length - 1] === trimmed) return prev;
        const next = [...prev, trimmed];
        if (next.length > 50) next.shift();
        return next;
      });
      setHistoryIndex(-1);
      setCommand("");
      await load();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : copy.sendFailed, "danger");
    } finally {
      setSending(false);
    }
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowUp") {
      event.preventDefault();
      if (history.length === 0) return;
      if (historyIndex === -1) {
        draftRef.current = command;
        const next = history.length - 1;
        setHistoryIndex(next);
        setCommand(history[next]);
      } else if (historyIndex > 0) {
        const next = historyIndex - 1;
        setHistoryIndex(next);
        setCommand(history[next]);
      }
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      if (historyIndex === -1) return;
      if (historyIndex < history.length - 1) {
        const next = historyIndex + 1;
        setHistoryIndex(next);
        setCommand(history[next]);
      } else {
        setHistoryIndex(-1);
        setCommand(draftRef.current);
      }
    } else if (event.key === "Escape") {
      event.preventDefault();
      setHistoryIndex(-1);
      setCommand("");
    }
  };

  const handleCommandChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    setCommand(event.target.value);
    if (historyIndex !== -1) setHistoryIndex(-1);
  };

  const toggleAutoScroll = () => {
    setAutoScroll((prev) => {
      const next = !prev;
      if (next) {
        const el = consoleRef.current;
        if (el) el.scrollTop = el.scrollHeight;
      }
      return next;
    });
  };

  const clear = () => setLines([]);

  const inputDisabled = server.observedPower !== "running" || sending;

  return (
    <div className="console-layout">
      <div className="terminal-panel">
        <div className="terminal-head">
          <div className="terminal-lights">
            <span className="terminal-light red" />
            <span className="terminal-light amber" />
            <span className="terminal-light green" />
          </div>
          <span>{server.name} / {copy.stream}</span>
          <div className="terminal-actions">
            <span className="console-sequence">{copy.sequence} {lines.at(-1)?.sequence ?? 0}</span>
            <button
              type="button"
              className={`terminal-tool${autoScroll ? " active" : ""}`}
              onClick={toggleAutoScroll}
              aria-pressed={autoScroll}
              title={copy.autoScroll}
              aria-label={copy.autoScroll}
            >
              <ArrowDownToLine size={14} />
            </button>
            <button
              type="button"
              className="terminal-tool"
              onClick={clear}
              title={copy.clearConsole}
              aria-label={copy.clearConsole}
            >
              <Trash2 size={14} />
            </button>
          </div>
        </div>
        <div className="terminal-output" ref={consoleRef} onScroll={handleScroll} translate="no">
          {lines.map((line) => (
            <div className={`terminal-line line-${line.stream}`} key={line.sequence}>
              <time>{formatConsoleTime(line.timestamp || line.receivedAt)}</time>
              <span>{line.message}</span>
            </div>
          ))}
          {!lines.length && <div className="terminal-empty">{copy.waiting}</div>}
        </div>
        <form className="console-command" onSubmit={submit}>
          <span>&gt;</span>
          <input
            value={command}
            onChange={handleCommandChange}
            onKeyDown={handleKeyDown}
            placeholder={server.observedPower === "running" ? copy.commandPlaceholder : copy.stoppedPlaceholder}
            disabled={inputDisabled}
            aria-label={copy.commandAria}
            name="console-command"
            autoComplete="off"
            spellCheck={false}
          />
          <button className="icon-button" disabled={inputDisabled || !command.trim()} aria-label={copy.send} title={copy.send}>
            <Send size={17} />
          </button>
        </form>
      </div>
      <aside className="console-side">
        <div className="panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">{copy.eyebrow}</p>
              <h2>{copy.connection}</h2>
            </div>
            <span className="live-dot" />
          </div>
          <dl className="detail-list compact-list">
            <div><dt>{copy.transport}</dt><dd>{copy.transportValue}</dd></div>
            <div><dt>{copy.lastSequence}</dt><dd translate="no">{lines.at(-1)?.sequence ?? "—"}</dd></div>
            <div><dt>{copy.snapshot}</dt><dd>{copy.lineCount(lines.length)}</dd></div>
            <div><dt>{copy.inputLimit}</dt><dd>{copy.characterCount(512)}</dd></div>
          </dl>
        </div>
        <div className="console-tip">
          <ShieldCheck size={17} />
          <span>
            <strong>{copy.scoped}</strong>
            <small>{copy.authorization}</small>
          </span>
        </div>
      </aside>
    </div>
  );
}

function fileTypeIcon(name: string): React.ReactNode {
  const lower = name.toLowerCase();
  if (lower.endsWith(".json")) return <Braces size={17} />;
  if (lower.endsWith(".yaml") || lower.endsWith(".yml") || lower.endsWith(".properties")) return <Settings2 size={17} />;
  if (lower.endsWith(".txt") || lower.endsWith(".log") || lower.endsWith(".md")) return <FileText size={17} />;
  if (lower.endsWith(".sh") || lower.endsWith(".js") || lower.endsWith(".ts") || lower.endsWith(".py") || lower.endsWith(".conf") || lower.endsWith(".cfg")) return <FileCode2 size={17} />;
  return <File size={17} />;
}

function FilesTab({ server, canWrite = true }: { server: Server; canWrite?: boolean }) {
  const copy = useCopy(filesCopy);
  const { locale } = useI18n();
  const { session, toast } = useAppContext();
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [loadedPath, setLoadedPath] = useState("");
  const [loadError, setLoadError] = useState("");
  const [loading, setLoading] = useState(true);
  const [editor, setEditor] = useState<{ path: string; content: string; originalContent: string; encoding: "utf-8" | "base64"; sizeBytes: number; modifiedAt: string } | null>(null);
  const [createMode, setCreateMode] = useState<"file" | "directory" | null>(null);
  const [entryName, setEntryName] = useState("");
  const [moveEntry, setMoveEntry] = useState<FileEntry | null>(null);
  const [destination, setDestination] = useState("");
  const [deleteEntry, setDeleteEntry] = useState<FileEntry | null>(null);
  const [busy, setBusy] = useState(false);
  const loadRequest = useRef(0);
  const load = useCallback(async () => {
    const requestId = ++loadRequest.current;
    const requestedPath = path;
    setLoading(true);
    setLoadError("");
    try {
      const nextEntries = await api.files(server.id, requestedPath);
      if (requestId === loadRequest.current) {
        setEntries(nextEntries);
        setLoadedPath(requestedPath);
      }
    } catch (reason) {
      if (requestId === loadRequest.current) {
        const message = reason instanceof Error ? reason.message : copy.loadError;
        setEntries([]);
        setLoadedPath(requestedPath);
        setLoadError(message);
        toast(message, "danger");
      }
    } finally {
      if (requestId === loadRequest.current) setLoading(false);
    }
  }, [copy.loadError, path, server.id, toast]);
  useEffect(() => { void load(); }, [load]);
  const parts = path ? path.split("/") : [];
  const childPath = (name: string) => path ? `${path}/${name.trim()}` : name.trim();
  const controlsLocked = busy || loading || loadedPath !== path;
  const mutationsLocked = controlsLocked || loadError !== "";
  const writeLocked = mutationsLocked || !canWrite;
  const navigateTo = (nextPath: string) => {
    if (controlsLocked || nextPath === path) return;
    setEntries([]);
    setLoadError("");
    setLoading(true);
    setPath(nextPath);
  };
  const openFile = async (entry: FileEntry) => {
    if (controlsLocked || loadError !== "") return;
    if (entry.kind === "directory") { navigateTo(entry.path); return; }
    setBusy(true);
    try {
      const content = await api.fileContent(server.id, entry.path);
      setEditor({ path: content.path, content: content.content, originalContent: content.content, encoding: content.encoding, sizeBytes: content.sizeBytes, modifiedAt: content.modifiedAt });
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : copy.readFailed, "danger");
    } finally { setBusy(false); }
  };
  const create = async () => {
    if (writeLocked) return;
    const requested = childPath(entryName);
    if (!requested || entries.some((entry) => entry.path === requested)) { toast(copy.duplicate, "warning"); return; }
    setBusy(true);
    try {
      if (createMode === "directory") await api.createDirectory(server.id, requested, session.csrfToken);
      else await api.writeFile(server.id, requested, "", session.csrfToken);
      toast(createMode === "directory" ? copy.directoryCreated : copy.fileCreated, "success");
      setCreateMode(null); setEntryName(""); await load();
    } catch (reason) { toast(reason instanceof Error ? reason.message : copy.createFailed, "danger"); }
    finally { setBusy(false); }
  };
  const save = async () => {
    if (writeLocked || !editor || editor.encoding !== "utf-8") return;
    setBusy(true);
    try { await api.writeFile(server.id, editor.path, editor.content, session.csrfToken); toast(copy.fileSaved, "success"); setEditor(null); await load(); }
    catch (reason) { toast(reason instanceof Error ? reason.message : copy.saveFailed, "danger"); }
    finally { setBusy(false); }
  };
  const move = async () => {
    if (writeLocked || !moveEntry || !destination.trim()) return;
    setBusy(true);
    try { await api.moveFile(server.id, moveEntry.path, destination.trim(), false, session.csrfToken); toast(copy.entryMoved, "success"); setMoveEntry(null); await load(); }
    catch (reason) { toast(reason instanceof Error ? reason.message : copy.moveFailed, "danger"); }
    finally { setBusy(false); }
  };
  const remove = async () => {
    if (writeLocked || !deleteEntry) return;
    setBusy(true);
    try { await api.deleteFile(server.id, deleteEntry.path, deleteEntry.kind === "directory", session.csrfToken); toast(copy.entryDeleted, "success"); setDeleteEntry(null); await load(); }
    catch (reason) { toast(reason instanceof Error ? reason.message : copy.deleteFailed, "danger"); }
    finally { setBusy(false); }
  };
  const isDirty = editor !== null && editor.content !== editor.originalContent;
  const editorName = editor?.path.split("/").at(-1) ?? "";
  const editorParts = editor ? editor.path.split("/") : [];
  return <>
    <div className="panel file-panel" aria-busy={controlsLocked}>
      <div className="file-toolbar">
        <div className="breadcrumbs" translate="no">
          <button type="button" onClick={() => navigateTo("")} disabled={controlsLocked}><HardDrive size={15} />server-data</button>
          {parts.map((part, index) => <span key={`${part}-${index}`}><ChevronRight size={13} /><button type="button" onClick={() => navigateTo(parts.slice(0, index + 1).join("/"))} disabled={controlsLocked}>{part}</button></span>)}
        </div>
        <div className="file-actions">
          {canWrite && <button type="button" className="icon-button" onClick={() => { setCreateMode("file"); setEntryName(""); }} disabled={writeLocked} aria-label={copy.newFileAria} title={copy.newFileTitle}><FilePlus2 size={16} /></button>}
          {canWrite && <button type="button" className="icon-button" onClick={() => { setCreateMode("directory"); setEntryName(""); }} disabled={writeLocked} aria-label={copy.newDirectoryAria} title={copy.newDirectoryTitle}><FolderPlus size={16} /></button>}
          <button type="button" className="icon-button" onClick={() => void load()} disabled={controlsLocked} aria-label={copy.refreshFilesAria} title={copy.refreshTitle}><RefreshCw className={loading ? "spin" : ""} size={16} /></button>
        </div>
      </div>
      <div className="file-list-head"><span>{copy.name}</span><span>{copy.size}</span><span>{copy.modified}</span><span>{copy.actions}</span></div>
      <div className="file-list">
        {entries.map((entry) => <div className="file-row" key={entry.path}>
          <button type="button" className="file-open" onClick={() => void openFile(entry)} disabled={controlsLocked || loadError !== ""}>
            <span className={`file-icon file-${entry.kind}`}>{entry.kind === "directory" ? <Folder size={17} fill="currentColor" /> : fileTypeIcon(entry.name)}</span>
            <span className="file-name" translate="no"><strong>{entry.name}</strong><small title={entry.path}>{entry.path}</small></span>
          </button>
          <span>{entry.kind === "directory" ? "—" : formatBytes(entry.sizeBytes)}</span>
          <span>{localizedDateTime(entry.modifiedAt, locale)}</span>
          <span className="file-row-actions">
            {canWrite && <button type="button" className="icon-button" onClick={() => { setMoveEntry(entry); setDestination(entry.path); }} disabled={writeLocked} aria-label={copy.moveAria(entry.name)} title={copy.moveTitle}><Move size={15} /></button>}
            {canWrite && <button type="button" className="icon-button danger-button" onClick={() => setDeleteEntry(entry)} disabled={writeLocked} aria-label={copy.deleteAria(entry.name)} title={copy.deleteTitle}><Trash2 size={15} /></button>}
            {entry.kind === "directory" && <button type="button" className="icon-button" onClick={() => navigateTo(entry.path)} disabled={controlsLocked || loadError !== ""} aria-label={copy.openAria(entry.name)} title={copy.openTitle}><ChevronRight size={15} /></button>}
          </span>
        </div>)}
        {!loading && loadError && <div className="empty-state" role="alert"><CircleAlert size={24} /><strong>{copy.unableDirectory}</strong><span>{loadError}</span><button type="button" className="button secondary" onClick={() => void load()}><RefreshCw size={16} />{copy.retry}</button></div>}
        {!loading && !loadError && !entries.length && <div className="empty-state"><Folder size={24} /><strong>{copy.emptyDirectory}</strong><span>{copy.emptyDetail}</span></div>}
      </div>
      <footer className="file-foot"><span>{copy.entries(entries.length)}</span><span>{copy.pathBoundary}</span></footer>
    </div>
    <Modal open={createMode !== null && canWrite} title={createMode === "directory" ? copy.createDirectory : copy.createFile} description={`${copy.location}: /${path}`} onClose={() => setCreateMode(null)} dismissible={!busy} footer={<><button type="button" className="button secondary" onClick={() => setCreateMode(null)} disabled={busy}>{copy.cancel}</button><button type="button" className="button primary" onClick={() => void create()} disabled={busy || !entryName.trim() || !canWrite}>{createMode === "directory" ? <FolderPlus size={16} /> : <FilePlus2 size={16} />}{copy.create}</button></>}><div className="modal-form"><label>{copy.name}<input value={entryName} onChange={(event) => setEntryName(event.target.value)} autoFocus maxLength={128} disabled={busy || !canWrite} /></label></div></Modal>
    <Modal open={editor !== null} title={editorName || copy.editor} description={editor ? `/${editor.path}` : ""} onClose={() => setEditor(null)} dismissible={!busy} footer={<><button type="button" className="button secondary" onClick={() => setEditor(null)} disabled={busy}>{copy.close}</button><button type="button" className="button primary" onClick={() => void save()} disabled={busy || !canWrite || editor?.encoding !== "utf-8" || !isDirty}><Save size={16} />{copy.save}</button></>}>{editor?.encoding === "base64" ? <div className="binary-file-note"><ShieldCheck size={20} /><div><strong>{copy.binaryFile}</strong><span>{copy.binaryDetail}</span></div></div> : editor && <div className="editor-wrapper"><div className="editor-breadcrumb" translate="no">{editorParts.slice(0, -1).map((part, index) => <span key={index}><span className="editor-breadcrumb-part">{part}</span><ChevronRight size={12} /></span>)}<span className="editor-breadcrumb-current">{isDirty && <span className="editor-dirty-dot" title={copy.unsaved} aria-label={copy.unsaved}><Circle size={8} fill="currentColor" /></span>}{editorName}</span></div><div className="editor-meta"><span>{formatBytes(editor.sizeBytes)}</span><i /><span>{localizedDateTime(editor.modifiedAt, locale)}</span><i /><span className={isDirty ? "editor-status-dirty" : "editor-status-saved"}>{isDirty ? copy.unsaved : copy.saved}</span></div><FileEditor value={editor.content} onChange={(next) => setEditor((current) => current ? { ...current, content: next } : current)} fileName={editorName} readOnly={busy || !canWrite} ariaLabel={copy.contentAria} /></div>}</Modal>
    <Modal open={moveEntry !== null && canWrite} title={copy.moveOrRename} description={moveEntry ? `${copy.current}: /${moveEntry.path}` : ""} onClose={() => setMoveEntry(null)} dismissible={!busy} footer={<><button type="button" className="button secondary" onClick={() => setMoveEntry(null)} disabled={busy}>{copy.cancel}</button><button type="button" className="button primary" onClick={() => void move()} disabled={busy || !canWrite || !destination.trim() || destination.trim() === moveEntry?.path}><Move size={16} />{copy.move}</button></>}><div className="modal-form"><label>{copy.destination}<input value={destination} onChange={(event) => setDestination(event.target.value)} autoFocus disabled={busy || !canWrite} /></label></div></Modal>
    <Modal open={deleteEntry !== null && canWrite} title={copy.deleteEntry} description={copy.deleteDescription} onClose={() => setDeleteEntry(null)} dismissible={!busy} footer={<><button type="button" className="button secondary" onClick={() => setDeleteEntry(null)} disabled={busy}>{copy.cancel}</button><button type="button" className="button danger-solid" onClick={() => void remove()} disabled={busy || !canWrite}><Trash2 size={16} />{copy.deleteTitle}</button></>}><div className="danger-confirm"><CircleAlert size={22} /><div><strong>{deleteEntry?.name}</strong><span>/{deleteEntry?.path}{deleteEntry?.kind === "directory" ? copy.andChildren : ""}</span></div></div></Modal>
  </>;
}

function BackupsTab({ server, canCreate = true, canRestore = true, canDelete = true }: { server: Server; canCreate?: boolean; canRestore?: boolean; canDelete?: boolean }) {
  const copy = useCopy(backupsCopy);
  const { locale } = useI18n();
  const { session, toast } = useAppContext();
  const [backups, setBackups] = useState<Backup[]>([]);
  const [busy, setBusy] = useState("");
  const [downloadingId, setDownloadingId] = useState("");
  const [activeOperation, setActiveOperation] = useState<Operation | null>(null);
  const [operationError, setOperationError] = useState("");
  const [restoreTarget, setRestoreTarget] = useState<Backup | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Backup | null>(null);
  const pollAbort = useRef<AbortController | null>(null);
  const load = useCallback(() => api.backups(server.id).then(setBackups).catch((reason) => toast(reason instanceof Error ? reason.message : copy.loadError, "danger")), [copy.loadError, server.id, toast]);
  const transitionActive = backups.some((backup) => ["creating", "restoring", "deleting"].includes(backup.status));
  const locked = busy !== "" || activeOperation !== null || transitionActive;
  useEffect(() => { void load(); }, [load]);
  useEffect(() => () => pollAbort.current?.abort(), []);
  useEffect(() => {
    if (!transitionActive || activeOperation) return;
    const timer = window.setInterval(() => void load(), 1000);
    return () => window.clearInterval(timer);
  }, [activeOperation, load, transitionActive]);
  const finishOperation = async (operation: Operation, successMessage: string) => {
    setActiveOperation(operation);
    setOperationError("");
    pollAbort.current?.abort();
    const controller = new AbortController();
    pollAbort.current = controller;
    try {
      const completed = await pollOperation(operation, api.operation, undefined, setActiveOperation, { signal: controller.signal });
      await load();
      if (completed.status === "succeeded") toast(successMessage, "success");
      else toast(operationFailureMessage(completed, copy.terminal(copy.operationStatus[completed.status])), "danger");
      setActiveOperation(null);
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === "AbortError")) {
        setOperationError(copy.operationUnavailable);
      }
      throw reason;
    } finally {
      if (pollAbort.current === controller) pollAbort.current = null;
    }
  };
  const create = async () => {
    if (!canCreate || locked) return;
    setBusy("create");
    setOperationError("");
    try {
      const operation = await api.createBackup(server.id, session.csrfToken);
      toast(copy.acceptedCreate, "warning");
      await finishOperation(operation, copy.created);
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === "AbortError")) toast(reason instanceof Error ? reason.message : copy.requestFailed, "danger");
    } finally {
      setBusy("");
    }
  };
  const restore = async () => {
    if (!canRestore || !restoreTarget || locked) return;
    const target = restoreTarget;
    setBusy(`restore:${target.id}`);
    setOperationError("");
    try {
      const operation = await api.restoreBackup(server.id, target.id, session.csrfToken);
      toast(copy.acceptedRestore, "warning");
      setRestoreTarget(null);
      await load();
      await finishOperation(operation, copy.restored);
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === "AbortError")) toast(reason instanceof Error ? reason.message : copy.restoreFailed, "danger");
    } finally {
      setBusy("");
    }
  };
  const remove = async () => {
    if (!canDelete || !deleteTarget || locked) return;
    const target = deleteTarget;
    const cleaningFailedBackup = target.status === "failed";
    setBusy(`delete:${target.id}`);
    setOperationError("");
    try {
      const operation = await api.deleteBackup(server.id, target.id, session.csrfToken);
      toast(cleaningFailedBackup ? copy.acceptedCleanup : copy.acceptedDelete, "warning");
      setDeleteTarget(null);
      await load();
      await finishOperation(operation, cleaningFailedBackup ? copy.cleaned : copy.deleted);
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === "AbortError")) toast(reason instanceof Error ? reason.message : cleaningFailedBackup ? copy.cleanupFailed : copy.deleteFailed, "danger");
    } finally {
      setBusy("");
    }
  };
  const retryOperationStatus = async () => {
    if (!activeOperation || busy) return;
    setBusy("status");
    const successMessage = activeOperation.type === "restore" ? copy.restored : activeOperation.type === "backup-delete" ? copy.deleted : copy.created;
    try {
      await finishOperation(activeOperation, successMessage);
    } catch (reason) {
      if (!(reason instanceof Error && reason.name === "AbortError")) toast(reason instanceof Error ? reason.message : copy.statusFailed, "danger");
    } finally {
      setBusy("");
    }
  };
  const copyChecksum = async (backup: Backup) => {
    if (!backup.checksum) return;
    try {
      await navigator.clipboard.writeText(backup.checksum);
      toast(copy.checksumCopied, "success");
    } catch {
      toast(copy.checksumCopyFailed, "danger");
    }
  };
  const download = async (backup: Backup) => {
    if (backup.status !== "ready" || downloadingId !== "") return;
    setDownloadingId(backup.id);
    try {
      await api.downloadBackup(server.id, backup.id);
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : copy.downloadFailed, "danger");
    } finally {
      setDownloadingId("");
    }
  };
  const restoreBlocked = server.observedPower !== "stopped";
  return <>
    <div className="backup-toolbar">
      <div>
        <p className="eyebrow">{copy.eyebrow}</p>
        <h2>{copy.title}</h2>
        <span>{copy.count(backups.length)}</span>
        {restoreBlocked && <span className="backup-lock-note" id="backup-restore-help">{copy.stopBeforeRestore}</span>}
        {activeOperation && <span className="operation-progress" role="status">{activeOperation.type === "restore" ? copy.operationName.restore : activeOperation.type === "backup-delete" ? copy.operationName["backup-delete"] : copy.operationName.backup} · {activeOperation.checkpoint} · {activeOperation.progress}% · {copy.attempt} {activeOperation.attempt}/{activeOperation.maxAttempts}</span>}
        {operationError && <span className="operation-error" role="alert">{operationError}</span>}
        {operationError && <button type="button" className="button secondary operation-retry" onClick={() => void retryOperationStatus()} disabled={busy !== ""}><RefreshCw size={15} />{copy.retryStatus}</button>}
        {!activeOperation && transitionActive && <span className="operation-progress" role="status">{copy.transitionRunning}</span>}
      </div>
      {canCreate && <button type="button" className="button primary" onClick={() => void create()} disabled={locked}><Archive size={16} />{busy === "create" ? copy.creating(activeOperation?.progress ?? 0) : copy.createBackup}</button>}
    </div>
    <div className="backup-list" aria-busy={locked}>
      {backups.map((backup) => <article className="backup-row" key={backup.id}>
        <span className="backup-icon"><Archive size={19} /></span>
        <div className="backup-name">
          <strong>{backup.name}</strong>
          <span className="backup-checksum">
            <code translate="no" title={backup.checksum ?? copy.checksumPending}>{backup.checksum ?? copy.checksumPending}</code>
            {backup.checksum && <button type="button" className="icon-button" onClick={() => void copyChecksum(backup)} aria-label={copy.copyChecksum(backup.name)} title={copy.copyChecksum(backup.name)}><Clipboard size={13} /></button>}
          </span>
          {backup.failureMessage && <small className="backup-failure" title={backup.failureMessage}>{backup.failureCode ? `${backup.failureCode}: ` : ""}{backup.failureMessage}</small>}
        </div>
        <StatusBadge tone={backup.status === "ready" ? "success" : backup.status === "failed" ? "danger" : "warning"}>{copy.status[backup.status]}</StatusBadge>
        <div className="backup-meta"><span>{backup.sizeBytes == null ? copy.pending : formatBytes(backup.sizeBytes)}</span><small>{localizedDateTime(backup.createdAt, locale)}</small></div>
        <div className="backup-actions">
          <button type="button" className="icon-button" onClick={() => void download(backup)} disabled={locked || backup.status !== "ready" || downloadingId !== ""} aria-label={copy.downloadBackup} title={downloadingId === backup.id ? copy.downloading : copy.downloadBackup}><Download size={15} className={downloadingId === backup.id ? "spin" : ""} /></button>
          {canRestore && <button type="button" className="icon-button" onClick={() => setRestoreTarget(backup)} disabled={locked || backup.status !== "ready" || restoreBlocked} aria-label={copy.restoreAria(backup.name)} aria-describedby={restoreBlocked ? "backup-restore-help" : undefined} title={restoreBlocked ? copy.stopServer : copy.restoreBackup}><RotateCcw size={15} /></button>}
          {canDelete && <button type="button" className="icon-button danger-button" onClick={() => setDeleteTarget(backup)} disabled={locked || !["ready", "failed"].includes(backup.status)} aria-label={backup.status === "failed" ? copy.cleanupAria(backup.name) : copy.deleteAria(backup.name)} title={backup.status === "failed" ? copy.cleanupFailedBackup : copy.deleteBackup}><Trash2 size={15} /></button>}
        </div>
      </article>)}
      {!backups.length && <div className="empty-state"><Archive size={25} /><strong>{copy.emptyTitle}</strong><span>{copy.emptyDetail}</span>{canCreate && <button type="button" className="button secondary" onClick={() => void create()} disabled={locked}><Archive size={16} />{busy === "create" ? copy.creatingShort : copy.createBackup}</button>}</div>}
    </div>
    <Modal open={restoreTarget !== null && canRestore} title={copy.restoreTitle} description={copy.restoreDescription} onClose={() => setRestoreTarget(null)} dismissible={!locked} footer={<><button type="button" className="button secondary" onClick={() => setRestoreTarget(null)} disabled={locked}>{copy.cancel}</button><button type="button" className="button primary" onClick={() => void restore()} disabled={locked || restoreBlocked || !canRestore}><RotateCcw size={16} />{copy.restore}</button></>}><div className="danger-confirm"><RotateCcw size={22} /><div><strong>{restoreTarget?.name}</strong><span>{restoreTarget?.checksum ?? copy.checksumUnavailable}</span></div></div></Modal>
    <Modal open={deleteTarget !== null && canDelete} title={deleteTarget?.status === "failed" ? copy.cleanupTitle : copy.deleteTitle} description={deleteTarget?.status === "failed" ? copy.cleanupDescription : copy.deleteDescription} onClose={() => setDeleteTarget(null)} dismissible={!locked} footer={<><button type="button" className="button secondary" onClick={() => setDeleteTarget(null)} disabled={locked}>{copy.cancel}</button><button type="button" className="button danger-solid" onClick={() => void remove()} disabled={locked || !canDelete}><Trash2 size={16} />{deleteTarget?.status === "failed" ? copy.cleanup : copy.deleteBackup}</button></>}><div className="danger-confirm"><CircleAlert size={22} /><div><strong>{deleteTarget?.name}</strong><span>{deleteTarget ? localizedDateTime(deleteTarget.createdAt, locale) : ""}</span></div></div></Modal>
  </>;
}

function NetworkTab({ server, refreshServer, canWrite = true }: { server: Server; refreshServer: () => Promise<void>; canWrite?: boolean }) {
  const copy = useCopy(networkCopy);
  const { locale } = useI18n();
  const { session, toast } = useAppContext();
  const [allocations, setAllocations] = useState<Allocation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [activeOperation, setActiveOperation] = useState<Operation | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Allocation | null>(null);
  const [bindIp, setBindIp] = useState("");
  const [port, setPort] = useState("25565");
  const [protocol, setProtocol] = useState<"tcp" | "udp">("tcp");
  const [makePrimary, setMakePrimary] = useState(false);
  const pollAbort = useRef<AbortController | null>(null);
  const load = useCallback(async () => {
    setLoading(true);
    try {
      setAllocations(await api.allocations(server.id));
      setError("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : copy.loadError);
    } finally {
      setLoading(false);
    }
  }, [copy.loadError, server.id]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => () => pollAbort.current?.abort(), []);

  const runReconcile = async (label: string, request: () => Promise<Operation>, successMessage: string): Promise<boolean> => {
    if (!canWrite || busy) return false;
    setBusy(label);
    setError("");
    pollAbort.current?.abort();
    const controller = new AbortController();
    pollAbort.current = controller;
    try {
      const accepted = await request();
      setActiveOperation(accepted);
      const completed = await pollOperation(accepted, api.operation, undefined, setActiveOperation, { signal: controller.signal });
      if (completed.status !== "succeeded") throw new Error(operationFailureMessage(completed, copy.reconcileFallback));
      await load();
      await refreshServer();
      toast(successMessage, "success");
      return true;
    } catch (reason) {
      if (reason instanceof Error && reason.name === "AbortError") return false;
      const message = reason instanceof ApiError && reason.code === "PRECONDITION_FAILED"
         ? copy.configChanged
         : reason instanceof Error ? reason.message : copy.updateFailed;
      setError(message);
      if (reason instanceof ApiError && reason.code === "PRECONDITION_FAILED") {
        await load();
        await refreshServer();
      }
      toast(message, "danger");
      return false;
    } finally {
      if (pollAbort.current === controller) pollAbort.current = null;
      setActiveOperation(null);
      setBusy("");
    }
  };

  const openCreate = () => {
    const primary = allocations.find((allocation) => allocation.primary) ?? allocations[0];
    setBindIp(primary?.bindIp ?? "127.0.0.1");
    setPort(String(Math.min(65535, (primary?.port ?? 25564) + 1)));
    setProtocol(primary?.protocol ?? "tcp");
    setMakePrimary(allocations.length === 0);
    setCreateOpen(true);
  };
  const create = async () => {
    const parsedPort = Number(port);
    if (!bindIp.trim() || !Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65535) {
      setError(copy.invalidEndpoint);
      return;
    }
    if (await runReconcile("create", () => api.createAllocation(server.id, { bindIp: bindIp.trim(), port: parsedPort, protocol, primary: makePrimary }, server.generation, session.csrfToken), copy.allocationAdded)) {
      setCreateOpen(false);
    }
  };
  const setPrimary = (allocation: Allocation) => runReconcile(`primary:${allocation.id}`, () => api.setPrimaryAllocation(server.id, allocation.id, server.generation, session.csrfToken), copy.primaryChanged);
  const remove = async () => {
    if (!deleteTarget) return;
    const target = deleteTarget;
    if (await runReconcile(`delete:${target.id}`, () => api.deleteAllocation(server.id, target.id, server.generation, session.csrfToken), copy.allocationReleased)) {
      setDeleteTarget(null);
    }
  };
  const locked = busy !== "" || activeOperation !== null || server.nodeCondition !== "available";

  return <>
    <div className="config-toolbar">
      <div><p className="eyebrow">{copy.eyebrow}</p><h2>{copy.title}</h2><span>{copy.active(allocations.length, server.generation)}</span></div>
      {canWrite && <button type="button" className="button primary" onClick={openCreate} disabled={locked || loading}><Plus size={16} />{copy.add}</button>}
    </div>
    {activeOperation && <div className="config-operation" role="status"><RefreshCw className="spin" size={15} /><span><strong>{copy.reconciling}</strong><small><span translate="no">{activeOperation.checkpoint}</span> · {activeOperation.progress}% · {copy.attempt} {activeOperation.attempt}/{activeOperation.maxAttempts} · {copy.generation} {activeOperation.generation}</small></span></div>}
    {error && <div className="config-alert" role="alert"><CircleAlert size={17} /><span>{error}</span><button type="button" className="button secondary" onClick={() => { void load(); refreshServer(); }} disabled={loading}><RefreshCw size={15} />{copy.reload}</button></div>}
    <div className="panel allocation-panel" aria-busy={loading || busy !== ""}>
      <div className="allocation-list-head"><span>{copy.endpoint}</span><span>{copy.protocol}</span><span>{copy.role}</span><span>{copy.updated}</span><span>{copy.actions}</span></div>
      <div className="allocation-list">
        {allocations.map((allocation) => <div className="allocation-row" key={allocation.id}>
          <div className="allocation-endpoint" translate="no"><span className="allocation-signal"><Network size={17} /></span><span><strong>{formatAllocationEndpoint(allocation)}</strong><small>{allocation.id}</small></span></div>
          <code className={`protocol-chip protocol-${allocation.protocol}`} translate="no">{allocation.protocol.toUpperCase()}</code>
           <span>{allocation.primary ? <span className="primary-allocation"><Star size={13} fill="currentColor" />{copy.primary}</span> : <span className="secondary-allocation">{copy.secondary}</span>}</span>
           <time>{localizedDateTime(allocation.updatedAt, locale)}</time>
          <span className="allocation-actions">
              {canWrite && <button type="button" className="icon-button" onClick={() => void setPrimary(allocation)} disabled={locked || allocation.primary} aria-label={copy.setPrimary(formatAllocationEndpoint(allocation))} title={copy.setPrimaryTitle}><Star size={15} /></button>}
              {canWrite && <button type="button" className="icon-button danger-button" onClick={() => setDeleteTarget(allocation)} disabled={locked || allocation.primary} aria-label={copy.deleteAria(formatAllocationEndpoint(allocation))} title={allocation.primary ? copy.primaryLocked : copy.deleteTitle}><Trash2 size={15} /></button>}
          </span>
        </div>)}
         {!loading && allocations.length === 0 && <div className="empty-state"><Network size={25} /><strong>{copy.none}</strong></div>}
         {loading && allocations.length === 0 && <LoadingState label={copy.loading} />}
      </div>
    </div>
    <Modal open={createOpen && canWrite} title={copy.addTitle} description={copy.nodeGeneration(server.nodeName, server.generation)} onClose={() => setCreateOpen(false)} dismissible={!busy} footer={<><button type="button" className="button secondary" onClick={() => setCreateOpen(false)} disabled={busy !== ""}>{copy.cancel}</button><button type="button" className="button primary" onClick={() => void create()} disabled={busy !== "" || !canWrite || !bindIp.trim() || !port}><Plus size={16} />{copy.addAllocation}</button></>}>
      <div className="modal-form"><div className="form-two-col"><label>{copy.bindIp}<input value={bindIp} onChange={(event) => setBindIp(event.target.value)} autoFocus disabled={busy !== ""} /></label><label>{copy.port}<input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(event.target.value)} disabled={busy !== ""} /></label></div><label>{copy.protocol}<select value={protocol} onChange={(event) => setProtocol(event.target.value as "tcp" | "udp")} disabled={busy !== ""}><option value="tcp">{copy.tcp}</option><option value="udp">{copy.udp}</option></select></label><label className="check-field"><input type="checkbox" checked={makePrimary} onChange={(event) => setMakePrimary(event.target.checked)} disabled={busy !== ""} /><span><strong>{copy.makePrimary}</strong><small>{copy.makePrimaryHint}</small></span></label></div>
    </Modal>
    <Modal open={deleteTarget !== null && canWrite} title={copy.releaseTitle} description={copy.releaseDescription} onClose={() => setDeleteTarget(null)} dismissible={!busy} footer={<><button type="button" className="button secondary" onClick={() => setDeleteTarget(null)} disabled={busy !== ""}>{copy.cancel}</button><button type="button" className="button danger-solid" onClick={() => void remove()} disabled={busy !== "" || !canWrite}><Trash2 size={16} />{copy.release}</button></>}><div className="danger-confirm"><CircleAlert size={22} /><div><strong>{deleteTarget ? formatAllocationEndpoint(deleteTarget) : ""}</strong><span>{deleteTarget?.protocol.toUpperCase()} / {copy.generation} {server.generation}</span></div></div></Modal>
  </>;
}

function formatAllocationEndpoint(allocation: Allocation): string {
  return allocation.bindIp.includes(":") ? `[${allocation.bindIp}]:${allocation.port}` : `${allocation.bindIp}:${allocation.port}`;
}

function createStartupRecord<T>(): Record<string, T> {
  return Object.create(null) as Record<string, T>;
}

function copyStartupRecord<T>(source: Record<string, T>): Record<string, T> {
  return Object.assign(createStartupRecord<T>(), source);
}

function StartupTab({ server, refreshServer, canWrite = true }: { server: Server; refreshServer: () => Promise<void>; canWrite?: boolean }) {
  const copy = useCopy(startupCopy);
  const { session, toast } = useAppContext();
  const [startup, setStartup] = useState<Startup | null>(null);
  const [form, setForm] = useState<Record<string, string | boolean>>(() => createStartupRecord());
  const [dirty, setDirty] = useState<Set<string>>(new Set());
  const [clearedVariables, setClearedVariables] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [activeOperation, setActiveOperation] = useState<Operation | null>(null);
  const pollAbort = useRef<AbortController | null>(null);
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const next = await api.startup(server.id);
      const nextForm = createStartupRecord<string | boolean>();
      const nextDirty = new Set<string>();
      next.variables.forEach((variable) => {
        if (variable.secret) nextForm[variable.key] = "";
        else if (variable.type === "boolean") nextForm[variable.key] = Boolean(variable.value ?? variable.default ?? variable.constValue ?? false);
        else nextForm[variable.key] = String(variable.value ?? variable.default ?? variable.constValue ?? "");
        if (!variable.hasValue && variable.constValue !== undefined) nextDirty.add(variable.key);
      });
      setStartup(next);
      setForm(nextForm);
      setDirty(nextDirty);
      setClearedVariables(new Set());
      setError("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : copy.loadError);
    } finally {
      setLoading(false);
    }
  }, [copy.loadError, server.id]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => () => pollAbort.current?.abort(), []);

  const updateField = (variable: StartupVariable, value: string | boolean) => {
    if (!canWrite) return;
    setForm((current) => {
      const next = copyStartupRecord(current);
      next[variable.key] = value;
      return next;
    });
    setClearedVariables((current) => { const next = new Set(current); next.delete(variable.key); return next; });
    setDirty((current) => {
      const next = new Set(current);
      if (variable.secret && value === "") next.delete(variable.key);
      else next.add(variable.key);
      return next;
    });
  };
  const clearVariable = (variable: StartupVariable) => {
    if (!canWrite || variable.required) return;
    setForm((current) => {
      const next = copyStartupRecord(current);
      next[variable.key] = "";
      return next;
    });
    setDirty((current) => new Set(current).add(variable.key));
    setClearedVariables((current) => new Set(current).add(variable.key));
  };
  const buildValues = (): Record<string, StartupValue> | null => {
    if (!startup) return null;
    const values = createStartupRecord<StartupValue>();
    for (const key of dirty) {
      const variable = startup.variables.find((candidate) => candidate.key === key);
      if (!variable) continue;
      if (clearedVariables.has(key)) { values[key] = null; continue; }
      const raw = form[key];
      if (variable.type === "integer") {
        const integerText = String(raw).trim();
        const value = Number(integerText);
        const invalidInteger = !/^-?(0|[1-9][0-9]*)$/.test(integerText) || !Number.isSafeInteger(value);
        const outsideMinimum = variable.minimum !== undefined && value < variable.minimum;
        const outsideMaximum = variable.maximum !== undefined && value > variable.maximum;
        if (invalidInteger || outsideMinimum || outsideMaximum) {
          setError(copy.integerRange(key));
          return null;
        }
        values[key] = value;
      } else if (variable.type === "boolean") values[key] = Boolean(raw);
      else {
        const value = String(raw);
        const length = Array.from(value).length;
        if (variable.required && value === "" || variable.minLength !== undefined && length < variable.minLength || variable.maxLength !== undefined && length > variable.maxLength || variable.enumValues && !variable.enumValues.includes(value)) {
          setError(copy.schema(key));
          return null;
        }
        values[key] = value;
      }
    }
    return values;
  };
  const save = async () => {
    if (!canWrite || busy || dirty.size === 0) return;
    const snapshot = startup;
    if (!snapshot) return;
    const values = buildValues();
    if (!values) return;
    setBusy(true);
    setError("");
    const controller = new AbortController();
    pollAbort.current?.abort();
    pollAbort.current = controller;
    try {
      const accepted = await api.updateStartup(server.id, values, snapshot.generation, session.csrfToken);
      setActiveOperation(accepted);
      const completed = await pollOperation(accepted, api.operation, undefined, setActiveOperation, { signal: controller.signal });
      if (completed.status !== "succeeded") throw new Error(operationFailureMessage(completed, copy.reconcileFallback(completed.status)));
      await load();
      await refreshServer();
      toast(copy.saved, "success");
    } catch (reason) {
      if (reason instanceof Error && reason.name === "AbortError") return;
      const message = reason instanceof ApiError && reason.code === "PRECONDITION_FAILED"
         ? copy.changed
         : reason instanceof Error ? reason.message : copy.updateFailed;
      setError(message);
      if (reason instanceof ApiError && reason.code === "PRECONDITION_FAILED") {
        await load();
        await refreshServer();
      }
      toast(message, "danger");
    } finally {
      if (pollAbort.current === controller) pollAbort.current = null;
      setActiveOperation(null);
      setBusy(false);
    }
  };

  if (loading && !startup) return <LoadingState label={copy.loading} />;
  if (!startup) return <ErrorState message={error || copy.unavailable} onRetry={() => void load()} />;
  const command = [startup.command.executable, ...startup.command.args].join(" ");
  return <div className="startup-layout">
    <div className="config-toolbar startup-toolbar"><div><p className="eyebrow">{copy.eyebrow}</p><h2>{copy.title}</h2><span>{copy.declared(startup.variables.length, startup.generation)}</span></div>{canWrite && <button type="button" className="button primary" onClick={() => void save()} disabled={busy || dirty.size === 0 || server.nodeCondition !== "available"}><Save size={16} />{copy.save}</button>}</div>
    {activeOperation && <div className="config-operation" role="status"><RefreshCw className="spin" size={15} /><span><strong>{copy.reconciling}</strong><small><span translate="no">{activeOperation.checkpoint}</span> · {activeOperation.progress}% · {copy.attempt} {activeOperation.attempt}/{activeOperation.maxAttempts} · {copy.generation} {activeOperation.generation}</small></span></div>}
    {error && <div className="config-alert" role="alert"><CircleAlert size={17} /><span>{error}</span><button type="button" className="button secondary" onClick={() => { void load(); refreshServer(); }} disabled={loading}><RefreshCw size={15} />{copy.reload}</button></div>}
    <div className="startup-grid">
      <section className="panel startup-command-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.resolved}</p><h3>{copy.process}</h3></div><Braces size={19} /></div><div className="startup-command" translate="no"><span>$</span><code>{command}</code></div><dl className="detail-list compact-list"><div><dt>{copy.executable}</dt><dd><code translate="no">{startup.command.executable}</code></dd></div><div><dt>{copy.arguments}</dt><dd>{startup.command.args.length}</dd></div><div><dt>{copy.definition}</dt><dd translate="no">{server.gameId}@{server.gameDefinitionVersion}</dd></div><div><dt>{copy.bundle}</dt><dd><code translate="no">{server.gameBundleDigest?.slice(0, 22)}...</code></dd></div></dl></section>
      <section className="panel startup-variable-panel"><div className="startup-variable-head"><div><p className="eyebrow">{copy.declaredSchema}</p><h3>{copy.variables}</h3></div><span>{dirty.size} {copy.pending}</span></div><div className="startup-variable-list">
        {startup.variables.map((variable) => <StartupVariableField key={variable.key} variable={variable} value={form[variable.key] ?? ""} busy={busy || !canWrite} pendingClear={clearedVariables.has(variable.key)} onChange={(value) => updateField(variable, value)} onClear={() => clearVariable(variable)} />)}
      </div></section>
    </div>
  </div>;
}

function StartupVariableField({ variable, value, busy, pendingClear, onChange, onClear }: { variable: StartupVariable; value: string | boolean; busy: boolean; pendingClear: boolean; onChange: (value: string | boolean) => void; onClear: () => void }) {
  const copy = useCopy(startupCopy);
  const hint = variable.secret
    ? variable.hasValue ? copy.secretConfigured : copy.secretMissing
    : variable.enumValues ? variable.enumValues.join(" / ")
      : variable.type === "integer" ? copy.integerHint(String(variable.minimum ?? "-∞"), String(variable.maximum ?? "∞"))
         : variable.required ? copy.requiredHint : copy.optionalHint;
  return <div className={`startup-variable${variable.secret ? " startup-secret" : ""}`}>
    <label><span className="startup-variable-label"><span>{variable.secret ? <LockKeyhole size={15} /> : variable.type === "boolean" ? <Check size={15} /> : <Braces size={15} />}<strong translate="no">{variable.key}</strong>{variable.required && <i>{copy.required}</i>}</span><small>{hint}</small></span>
      {variable.type === "boolean" ? <span className="toggle-field"><input aria-label={variable.key} type="checkbox" checked={Boolean(value)} onChange={(event) => onChange(event.target.checked)} disabled={busy || variable.constValue !== undefined} /><span /></span>
        : variable.enumValues ? <select aria-label={variable.key} value={String(value)} onChange={(event) => onChange(event.target.value)} disabled={busy || variable.constValue !== undefined} translate="no">{variable.enumValues.map((option) => <option value={option} key={option}>{option}</option>)}</select>
          : <input aria-label={variable.key} type={variable.secret ? "password" : variable.type === "integer" ? "number" : "text"} value={String(value)} min={variable.minimum} max={variable.maximum} onChange={(event) => onChange(event.target.value)} disabled={busy || variable.constValue !== undefined} autoComplete={variable.secret ? "new-password" : "off"} placeholder={variable.secret ? variable.hasValue ? copy.configuredReplacement : copy.enterSecret : undefined} />}
    </label>
    {!variable.required && <div className="secret-state"><span>{variable.secret ? <KeyRound size={13} /> : <RotateCcw size={13} />}{pendingClear ? copy.willClear : variable.secret ? variable.hasValue ? copy.configured : copy.notConfigured : variable.hasValue ? copy.configured : copy.usingDefault}</span><button type="button" className="button secondary" onClick={onClear} disabled={busy || !variable.hasValue}>{pendingClear ? copy.clearAgain : copy.clear}</button></div>}
  </div>;
}

function ActivityTab({ server }: { server: Server }) {
  const copy = useCopy(activityCopy);
  const { locale } = useI18n();
  const [operations, setOperations] = useState<Operation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const requestVersion = useRef(0);
  const load = useCallback(async () => {
    const version = ++requestVersion.current;
    setLoading(true);
    try {
      const items = await api.operations();
      if (version !== requestVersion.current) return;
      setOperations(items.filter((item) => item.serverId === server.id));
      setError("");
    } catch (reason) {
      if (version !== requestVersion.current) return;
       setError(reason instanceof Error ? reason.message : copy.loadError);
    } finally {
      if (version === requestVersion.current) setLoading(false);
    }
   }, [copy.loadError, server.id]);
  useEffect(() => {
    void load();
    return () => { requestVersion.current += 1; };
  }, [load]);

  return <div className="panel server-activity"><div className="panel-heading"><div><p className="eyebrow">{copy.eyebrow}</p><h2>{copy.title}</h2></div><button className="icon-button" onClick={() => void load()} disabled={loading} aria-label={copy.refresh} title={copy.refresh}><RefreshCw size={16} className={loading ? "is-spinning" : ""} /></button></div>{loading && !operations.length && <LoadingState label={copy.loading} />}{error && !operations.length && <ErrorState message={error} onRetry={() => void load()} />}{error && operations.length > 0 && <div className="inline-warning" role="alert"><CircleAlert size={15} />{error}<button onClick={() => void load()}>{copy.retry}</button></div>}{operations.map((operation) => { const tone = activityTone(operation.status); return <div className="server-activity-row" key={operation.id}><span className={`activity-mark ${tone}`}><span /></span><div><strong>{copy.operationType[operation.type]}</strong><span translate="no">{operation.checkpoint} · {operation.id.slice(0, 13)}...</span></div><time>{localizedDateTime(operation.updatedAt, locale)}</time><span className={`result-pill result-${tone}`}>{copy.status[operation.status]}</span></div>; })}{!loading && !error && !operations.length && <div className="empty-state"><Activity size={24} /><strong>{copy.emptyTitle}</strong><span>{copy.emptyDetail}</span></div>}</div>;
}

function activityTone(status: Operation["status"]): "accepted" | "success" | "failure" | "neutral" {
  if (status === "succeeded") return "success";
  if (status === "failed") return "failure";
  if (status === "canceled") return "neutral";
  return "accepted";
}

function SettingsTab({ server }: { server: Server }) {
  const copy = useCopy(settingsCopy);
  const lifecycle = copy.lifecycleState[server.lifecycleState];
  return <div className="settings-grid"><div className="panel settings-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.identity}</p><h2>{copy.details}</h2></div><ServerIcon size={19} /></div><dl className="detail-list"><div><dt>{copy.serverId}</dt><dd><code translate="no">{server.id}</code></dd></div><div><dt>{copy.owner}</dt><dd>{server.ownerName}</dd></div><div><dt>{copy.gameVersion}</dt><dd translate="no">{server.gameVersion}</dd></div><div><dt>{copy.definition}</dt><dd translate="no">{server.gameId}@{server.gameDefinitionVersion}</dd></div><div><dt>{copy.bundle}</dt><dd><code translate="no">{server.gameBundleDigest}</code></dd></div><div><dt>{copy.allocation}</dt><dd translate="no">{server.allocation}</dd></div></dl></div><div className="panel settings-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.desiredState}</p><h2>{copy.reconciliation}</h2></div><RefreshCw size={19} /></div><dl className="detail-list"><div><dt>{copy.lifecycle}</dt><dd>{lifecycle}</dd></div><div><dt>{copy.desiredPower}</dt><dd>{copy.powerStatus[server.desiredPower]}</dd></div><div><dt>{copy.observedPower}</dt><dd>{copy.powerStatus[server.observedPower]}</dd></div><div><dt>{copy.generation}</dt><dd>{server.generation}</dd></div><div><dt>{copy.observedGeneration}</dt><dd>{server.observedGeneration}</dd></div></dl></div></div>;
}
