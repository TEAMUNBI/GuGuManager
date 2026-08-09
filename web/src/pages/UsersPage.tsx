import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Check, Clipboard, KeyRound, Plus, RefreshCw, Search, ShieldCheck, ShieldOff, UserRound, UsersRound } from "lucide-react";
import { api, ApiError } from "../lib/api";
import type { CreateUserInput, GlobalRole, PasswordResetToken, Server, ServerMembership, ServerPermission, User } from "../lib/types";
import { Modal } from "../components/Modal";
import { ErrorState, LoadingState } from "../components/PageState";
import { useAppContext } from "../app/App";
import { type LocalizedCopy, useCopy, useI18n } from "../i18n/I18n";

type PermissionGroup = "core" | "files" | "backups" | "configuration";

const usersIntlLocales = { "zh-CN": "zh-CN", en: "en-GB", ja: "ja-JP", ko: "ko-KR" } as const;

const permissionOptions: Array<{ value: ServerPermission; group: PermissionGroup }> = [
  { value: "servers.read", group: "core" },
  { value: "servers.power", group: "core" },
  { value: "servers.console", group: "core" },
  { value: "servers.files.read", group: "files" },
  { value: "servers.files.write", group: "files" },
  { value: "servers.backups.read", group: "backups" },
  { value: "servers.backups.create", group: "backups" },
  { value: "servers.backups.restore", group: "backups" },
  { value: "servers.backups.delete", group: "backups" },
  { value: "servers.network.read", group: "configuration" },
  { value: "servers.network.write", group: "configuration" },
  { value: "servers.startup.read", group: "configuration" },
  { value: "servers.startup.write", group: "configuration" },
];

interface UsersCopy {
  page: { eyebrow: string; title: string; description: string; newUser: string; loading: string };
  summary: { aria: string; localUsers: string; active: string; admins: string };
  directory: { eyebrow: string; title: string; searchAria: string; searchPlaceholder: string; status: (status: string) => string; empty: string };
  statuses: Record<User["status"], string>;
  roles: Record<GlobalRole, string>;
  roleCodes: Record<GlobalRole, string>;
  detail: { aria: string; selectedEyebrow: string; issueReset: string; disable: string; enable: string; globalRoleEyebrow: string; platformScope: string; globalRoleAria: string; ownRoleHint: string; selectUser: string };
  membership: { eyebrow: string; title: string; globalAccess: string; globalDescription: string; server: string; serverAria: string; save: string; revoke: string; noServers: string; noServersDescription: string };
  permissions: Record<ServerPermission, string>;
  permissionGroups: Record<PermissionGroup, string>;
  feedback: {
    unableLoadUsers: string;
    selectedMembershipMismatch: string;
    unableReadAccess: string;
    noUserSelected: string;
    userEnabled: string;
    userDisabled: string;
    unableUpdateStatus: string;
    roleUpdated: string;
    unableUpdateRole: string;
    unableIssueReset: string;
    submittedMembershipMismatch: string;
    accessSaved: string;
    unableSaveAccess: string;
    accessRevoked: string;
    unableRevokeAccess: string;
    userCreated: string;
  };
  common: { cancel: string };
  disableDialog: { title: string; description: (email: string) => string; busy: string; confirm: string };
  demotionDialog: { title: string; description: string; busy: string; confirm: string };
  revokeDialog: { title: string; description: string; busy: string; confirm: string };
  createDialog: { title: string; description: string; busy: string; confirm: string; email: string; displayName: string; temporaryPassword: string; role: string; unableCreate: string };
  resetDialog: { title: string; description: (email: string) => string; copy: string; done: string; token: string; user: string; expires: string; copied: string; clipboardUnavailable: string };
}

const usersCopy: LocalizedCopy<UsersCopy> = {
  "zh-CN": {
    page: { eyebrow: "管理 / 身份与访问", title: "用户与访问", description: "管理本地身份与服务器级授权。", newUser: "新建用户", loading: "正在加载身份目录" },
    summary: { aria: "身份概览", localUsers: "本地用户", active: "已启用", admins: "平台管理员" },
    directory: { eyebrow: "用户目录", title: "本地用户", searchAria: "搜索用户", searchPlaceholder: "搜索", status: (status) => `状态：${status}`, empty: "没有符合搜索条件的用户。" },
    statuses: { active: "已启用", disabled: "已停用" },
    roles: { platform_admin: "平台管理员", server_owner: "服务器所有者" },
    roleCodes: { platform_admin: "管理员", server_owner: "所有者" },
    detail: { aria: "已选用户的访问权限", selectedEyebrow: "已选身份", issueReset: "签发重置令牌", disable: "停用", enable: "启用", globalRoleEyebrow: "全局角色", platformScope: "平台范围", globalRoleAria: "全局角色", ownRoleHint: "请使用其他平台管理员账号登录，以更改此账号的全局角色。", selectUser: "请选择本地用户。" },
    membership: { eyebrow: "服务器成员资格", title: "服务器级权限", globalAccess: "已启用全局访问", globalDescription: "平台管理员无需服务器成员资格即可访问。", server: "服务器", serverAria: "成员服务器", save: "保存访问权限", revoke: "撤销", noServers: "没有可用服务器", noServersDescription: "请先创建服务器，再分配成员资格。" },
    permissions: { "servers.read": "查看服务器", "servers.power": "电源控制", "servers.console": "控制台", "servers.files.read": "读取文件", "servers.files.write": "写入文件", "servers.backups.read": "查看备份", "servers.backups.create": "创建备份", "servers.backups.restore": "恢复备份", "servers.backups.delete": "删除备份", "servers.network.read": "查看网络", "servers.network.write": "编辑网络", "servers.startup.read": "查看启动配置", "servers.startup.write": "编辑启动配置" },
    permissionGroups: { core: "基本", files: "文件", backups: "备份", configuration: "配置" },
    feedback: { unableLoadUsers: "无法加载本地用户。", selectedMembershipMismatch: "成员资格响应与所选用户及服务器不匹配。", unableReadAccess: "无法读取服务器访问权限。", noUserSelected: "未选择本地用户。", userEnabled: "用户已启用", userDisabled: "用户已停用", unableUpdateStatus: "无法更新用户状态。", roleUpdated: "全局角色已更新", unableUpdateRole: "无法更新全局角色。", unableIssueReset: "无法签发重置令牌。", submittedMembershipMismatch: "成员资格响应与提交的用户及服务器不匹配。", accessSaved: "服务器访问权限已保存", unableSaveAccess: "无法保存服务器访问权限。", accessRevoked: "服务器访问权限已撤销", unableRevokeAccess: "无法撤销服务器访问权限。", userCreated: "本地用户已创建" },
    common: { cancel: "取消" },
    disableDialog: { title: "停用本地用户？", description: (email) => `停用 ${email} 后，该用户将无法再次登录，现有会话会立即撤销。`, busy: "正在停用…", confirm: "停用用户" },
    demotionDialog: { title: "移除平台管理员权限？", description: "该用户将失去全局用户、节点、游戏目录和审计管理权限。今后的服务器访问需要明确分配成员资格。", busy: "正在移除…", confirm: "移除管理员权限" },
    revokeDialog: { title: "撤销服务器访问权限？", description: "此成员资格将立即删除。后续请求中，该用户会立即失去访问权限。", busy: "正在撤销…", confirm: "撤销服务器访问权限" },
    createDialog: { title: "创建本地用户", description: "添加一个可登录 GuGuManager 的本地账号。", busy: "正在创建…", confirm: "创建用户", email: "邮箱地址", displayName: "显示名称", temporaryPassword: "临时密码", role: "权限角色", unableCreate: "无法创建本地用户。" },
    resetDialog: { title: "一次性重置令牌", description: (email) => `已为 ${email} 签发。此明文值不会再次显示。`, copy: "复制令牌", done: "完成", token: "重置令牌", user: "用户", expires: "过期时间", copied: "重置令牌已复制", clipboardUnavailable: "无法访问剪贴板" },
  },
  en: {
    page: { eyebrow: "ADMIN / IDENTITY & ACCESS", title: "Users & access", description: "Manage local identities and server-scoped permissions.", newUser: "New user", loading: "Loading identity directory" },
    summary: { aria: "Identity summary", localUsers: "local users", active: "active", admins: "platform admins" },
    directory: { eyebrow: "DIRECTORY", title: "Local users", searchAria: "Search users", searchPlaceholder: "Search", status: (status) => `Status: ${status}`, empty: "No users match this search." },
    statuses: { active: "active", disabled: "disabled" },
    roles: { platform_admin: "Platform admin", server_owner: "Server owner" },
    roleCodes: { platform_admin: "ADMIN", server_owner: "OWNER" },
    detail: { aria: "Selected user access", selectedEyebrow: "SELECTED IDENTITY", issueReset: "Issue reset token", disable: "Disable", enable: "Enable", globalRoleEyebrow: "GLOBAL ROLE", platformScope: "Platform scope", globalRoleAria: "Global role", ownRoleHint: "Sign in as a different platform administrator to change this account's global role.", selectUser: "Select a local user." },
    membership: { eyebrow: "SERVER MEMBERSHIP", title: "Scoped permissions", globalAccess: "Global access active", globalDescription: "Platform administrators bypass server memberships.", server: "Server", serverAria: "Membership server", save: "Save access", revoke: "Revoke", noServers: "No servers available", noServersDescription: "Create a server before assigning membership." },
    permissions: { "servers.read": "View server", "servers.power": "Power controls", "servers.console": "Console", "servers.files.read": "Read files", "servers.files.write": "Write files", "servers.backups.read": "View backups", "servers.backups.create": "Create backups", "servers.backups.restore": "Restore backups", "servers.backups.delete": "Delete backups", "servers.network.read": "View network", "servers.network.write": "Edit network", "servers.startup.read": "View startup", "servers.startup.write": "Edit startup" },
    permissionGroups: { core: "Core", files: "Files", backups: "Backups", configuration: "Configuration" },
    feedback: { unableLoadUsers: "Unable to load local users.", selectedMembershipMismatch: "Membership response did not match the selected user and server.", unableReadAccess: "Unable to read server access.", noUserSelected: "No local user is selected.", userEnabled: "User enabled", userDisabled: "User disabled", unableUpdateStatus: "Unable to update user status.", roleUpdated: "Global role updated", unableUpdateRole: "Unable to update global role.", unableIssueReset: "Unable to issue a reset token.", submittedMembershipMismatch: "Membership response did not match the submitted user and server.", accessSaved: "Server access saved", unableSaveAccess: "Unable to save server access.", accessRevoked: "Server access revoked", unableRevokeAccess: "Unable to revoke server access.", userCreated: "Local user created" },
    common: { cancel: "Cancel" },
    disableDialog: { title: "Disable local user?", description: (email) => `Disabling ${email} prevents future sign-ins. Existing sessions are revoked immediately.`, busy: "Disabling...", confirm: "Disable user" },
    demotionDialog: { title: "Remove platform administrator access?", description: "This user will lose global user, node, catalog, and audit administration. Future server access requires explicit memberships.", busy: "Removing...", confirm: "Remove admin access" },
    revokeDialog: { title: "Revoke server access?", description: "This membership will be removed immediately. The user will immediately lose access on subsequent requests.", busy: "Revoking...", confirm: "Revoke server access" },
    createDialog: { title: "Create local user", description: "Add a credential-backed identity to this control plane.", busy: "Creating...", confirm: "Create user", email: "Email", displayName: "Display name", temporaryPassword: "Temporary password", role: "Role", unableCreate: "Unable to create the local user." },
    resetDialog: { title: "One-time reset token", description: (email) => `Issued for ${email}. This plaintext value will not be shown again.`, copy: "Copy token", done: "Done", token: "RESET TOKEN", user: "User", expires: "Expires", copied: "Reset token copied", clipboardUnavailable: "Clipboard access is unavailable" },
  },
  ja: {
    page: { eyebrow: "管理 / ID とアクセス", title: "ユーザーとアクセス", description: "ローカル ID とサーバー単位の権限を管理します。", newUser: "新規ユーザー", loading: "ID ディレクトリを読み込み中" },
    summary: { aria: "ID の概要", localUsers: "ローカルユーザー", active: "有効", admins: "プラットフォーム管理者" },
    directory: { eyebrow: "ディレクトリ", title: "ローカルユーザー", searchAria: "ユーザーを検索", searchPlaceholder: "検索", status: (status) => `状態：${status}`, empty: "検索条件に一致するユーザーはいません。" },
    statuses: { active: "有効", disabled: "無効" },
    roles: { platform_admin: "プラットフォーム管理者", server_owner: "サーバー所有者" },
    roleCodes: { platform_admin: "管理者", server_owner: "所有者" },
    detail: { aria: "選択したユーザーのアクセス権", selectedEyebrow: "選択中の ID", issueReset: "リセットトークンを発行", disable: "無効化", enable: "有効化", globalRoleEyebrow: "グローバルロール", platformScope: "プラットフォーム範囲", globalRoleAria: "グローバルロール", ownRoleHint: "このアカウントのグローバルロールを変更するには、別のプラットフォーム管理者としてサインインしてください。", selectUser: "ローカルユーザーを選択してください。" },
    membership: { eyebrow: "サーバーメンバーシップ", title: "サーバー単位の権限", globalAccess: "グローバルアクセス有効", globalDescription: "プラットフォーム管理者にはサーバーメンバーシップが不要です。", server: "サーバー", serverAria: "メンバーシップのサーバー", save: "アクセス権を保存", revoke: "取り消す", noServers: "利用可能なサーバーがありません", noServersDescription: "メンバーシップを割り当てる前にサーバーを作成してください。" },
    permissions: { "servers.read": "サーバーを表示", "servers.power": "電源操作", "servers.console": "コンソール", "servers.files.read": "ファイルを読み取る", "servers.files.write": "ファイルを書き込む", "servers.backups.read": "バックアップを表示", "servers.backups.create": "バックアップを作成", "servers.backups.restore": "バックアップを復元", "servers.backups.delete": "バックアップを削除", "servers.network.read": "ネットワークを表示", "servers.network.write": "ネットワークを編集", "servers.startup.read": "起動設定を表示", "servers.startup.write": "起動設定を編集" },
    permissionGroups: { core: "基本", files: "ファイル", backups: "バックアップ", configuration: "設定" },
    feedback: { unableLoadUsers: "ローカルユーザーを読み込めません。", selectedMembershipMismatch: "メンバーシップの応答が選択したユーザーとサーバーに一致しません。", unableReadAccess: "サーバーのアクセス権を読み取れません。", noUserSelected: "ローカルユーザーが選択されていません。", userEnabled: "ユーザーを有効にしました", userDisabled: "ユーザーを無効にしました", unableUpdateStatus: "ユーザーの状態を更新できません。", roleUpdated: "グローバルロールを更新しました", unableUpdateRole: "グローバルロールを更新できません。", unableIssueReset: "リセットトークンを発行できません。", submittedMembershipMismatch: "メンバーシップの応答が送信したユーザーとサーバーに一致しません。", accessSaved: "サーバーのアクセス権を保存しました", unableSaveAccess: "サーバーのアクセス権を保存できません。", accessRevoked: "サーバーのアクセス権を取り消しました", unableRevokeAccess: "サーバーのアクセス権を取り消せません。", userCreated: "ローカルユーザーを作成しました" },
    common: { cancel: "キャンセル" },
    disableDialog: { title: "ローカルユーザーを無効にしますか？", description: (email) => `${email} を無効にすると、以後サインインできなくなり、既存のセッションは直ちに取り消されます。`, busy: "無効化中…", confirm: "ユーザーを無効化" },
    demotionDialog: { title: "プラットフォーム管理者権限を削除しますか？", description: "このユーザーは、ユーザー、ノード、カタログ、監査に関するグローバル管理権限を失います。今後サーバーへアクセスするには、明示的なメンバーシップが必要です。", busy: "削除中…", confirm: "管理者権限を削除" },
    revokeDialog: { title: "サーバーのアクセス権を取り消しますか？", description: "このメンバーシップは直ちに削除されます。以後のリクエストでは、ユーザーは直ちにアクセス権を失います。", busy: "取り消し中…", confirm: "サーバーアクセスを取り消す" },
    createDialog: { title: "ローカルユーザーを作成", description: "このコントロールプレーンに認証情報を持つ ID を追加します。", busy: "作成中…", confirm: "ユーザーを作成", email: "メールアドレス", displayName: "表示名", temporaryPassword: "一時パスワード", role: "ロール", unableCreate: "ローカルユーザーを作成できません。" },
    resetDialog: { title: "ワンタイムリセットトークン", description: (email) => `${email} に発行しました。この平文の値は再表示されません。`, copy: "トークンをコピー", done: "完了", token: "リセットトークン", user: "ユーザー", expires: "有効期限", copied: "リセットトークンをコピーしました", clipboardUnavailable: "クリップボードを利用できません" },
  },
  ko: {
    page: { eyebrow: "관리 / ID 및 접근", title: "사용자 및 접근", description: "로컬 ID와 서버별 권한을 관리합니다.", newUser: "새 사용자", loading: "ID 디렉터리를 불러오는 중" },
    summary: { aria: "ID 요약", localUsers: "로컬 사용자", active: "활성", admins: "플랫폼 관리자" },
    directory: { eyebrow: "디렉터리", title: "로컬 사용자", searchAria: "사용자 검색", searchPlaceholder: "검색", status: (status) => `상태: ${status}`, empty: "검색 조건과 일치하는 사용자가 없습니다." },
    statuses: { active: "활성", disabled: "비활성" },
    roles: { platform_admin: "플랫폼 관리자", server_owner: "서버 소유자" },
    roleCodes: { platform_admin: "관리자", server_owner: "소유자" },
    detail: { aria: "선택한 사용자의 접근 권한", selectedEyebrow: "선택한 ID", issueReset: "재설정 토큰 발급", disable: "비활성화", enable: "활성화", globalRoleEyebrow: "전역 역할", platformScope: "플랫폼 범위", globalRoleAria: "전역 역할", ownRoleHint: "이 계정의 전역 역할을 변경하려면 다른 플랫폼 관리자로 로그인하세요.", selectUser: "로컬 사용자를 선택하세요." },
    membership: { eyebrow: "서버 멤버십", title: "서버별 권한", globalAccess: "전역 접근 활성", globalDescription: "플랫폼 관리자는 서버 멤버십 없이도 접근할 수 있습니다.", server: "서버", serverAria: "멤버십 서버", save: "접근 권한 저장", revoke: "철회", noServers: "사용 가능한 서버 없음", noServersDescription: "멤버십을 할당하기 전에 서버를 생성하세요." },
    permissions: { "servers.read": "서버 보기", "servers.power": "전원 제어", "servers.console": "콘솔", "servers.files.read": "파일 읽기", "servers.files.write": "파일 쓰기", "servers.backups.read": "백업 보기", "servers.backups.create": "백업 만들기", "servers.backups.restore": "백업 복원", "servers.backups.delete": "백업 삭제", "servers.network.read": "네트워크 보기", "servers.network.write": "네트워크 편집", "servers.startup.read": "시작 설정 보기", "servers.startup.write": "시작 설정 편집" },
    permissionGroups: { core: "기본", files: "파일", backups: "백업", configuration: "설정" },
    feedback: { unableLoadUsers: "로컬 사용자를 불러올 수 없습니다.", selectedMembershipMismatch: "멤버십 응답이 선택한 사용자 및 서버와 일치하지 않습니다.", unableReadAccess: "서버 접근 권한을 읽을 수 없습니다.", noUserSelected: "선택한 로컬 사용자가 없습니다.", userEnabled: "사용자를 활성화했습니다", userDisabled: "사용자를 비활성화했습니다", unableUpdateStatus: "사용자 상태를 업데이트할 수 없습니다.", roleUpdated: "전역 역할을 업데이트했습니다", unableUpdateRole: "전역 역할을 업데이트할 수 없습니다.", unableIssueReset: "재설정 토큰을 발급할 수 없습니다.", submittedMembershipMismatch: "멤버십 응답이 제출한 사용자 및 서버와 일치하지 않습니다.", accessSaved: "서버 접근 권한을 저장했습니다", unableSaveAccess: "서버 접근 권한을 저장할 수 없습니다.", accessRevoked: "서버 접근 권한을 철회했습니다", unableRevokeAccess: "서버 접근 권한을 철회할 수 없습니다.", userCreated: "로컬 사용자를 생성했습니다" },
    common: { cancel: "취소" },
    disableDialog: { title: "로컬 사용자를 비활성화할까요?", description: (email) => `${email} 사용자를 비활성화하면 더 이상 로그인할 수 없으며 기존 세션은 즉시 철회됩니다.`, busy: "비활성화 중…", confirm: "사용자 비활성화" },
    demotionDialog: { title: "플랫폼 관리자 권한을 제거할까요?", description: "이 사용자는 사용자, 노드, 카탈로그 및 감사에 대한 전역 관리 권한을 잃습니다. 이후 서버에 접근하려면 명시적인 멤버십이 필요합니다.", busy: "제거 중…", confirm: "관리자 권한 제거" },
    revokeDialog: { title: "서버 접근 권한을 철회할까요?", description: "이 멤버십은 즉시 제거됩니다. 이후 요청부터 사용자는 즉시 접근 권한을 잃습니다.", busy: "철회 중…", confirm: "서버 접근 권한 철회" },
    createDialog: { title: "로컬 사용자 생성", description: "이 컨트롤 플레인에 자격 증명으로 로그인하는 ID를 추가합니다.", busy: "생성 중…", confirm: "사용자 생성", email: "이메일", displayName: "표시 이름", temporaryPassword: "임시 비밀번호", role: "역할", unableCreate: "로컬 사용자를 생성할 수 없습니다." },
    resetDialog: { title: "일회용 재설정 토큰", description: (email) => `${email} 사용자에게 발급했습니다. 이 평문 값은 다시 표시되지 않습니다.`, copy: "토큰 복사", done: "완료", token: "재설정 토큰", user: "사용자", expires: "만료", copied: "재설정 토큰을 복사했습니다", clipboardUnavailable: "클립보드에 접근할 수 없습니다" },
  },
};

interface IssuedResetToken extends PasswordResetToken {
  user: User;
}

type MutationResult = { ok: true } | { ok: false; message: string };

export function UsersPage() {
  const copy = useCopy(usersCopy);
  const { session, toast } = useAppContext();
  const [users, setUsers] = useState<User[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [selectedUserId, setSelectedUserId] = useState("");
  const [selectedServerId, setSelectedServerId] = useState("");
  const [membership, setMembership] = useState<ServerMembership | null>(null);
  const [membershipScope, setMembershipScope] = useState("");
  const [permissions, setPermissions] = useState<ServerPermission[]>(["servers.read"]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [membershipLoading, setMembershipLoading] = useState(false);
  const [busyAction, setBusyAction] = useState("");
  const [error, setError] = useState("");
  const [membershipError, setMembershipError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [issuedToken, setIssuedToken] = useState<IssuedResetToken | null>(null);
  const [pendingDisable, setPendingDisable] = useState<User | null>(null);
  const [pendingDemotion, setPendingDemotion] = useState<User | null>(null);
  const [pendingRevoke, setPendingRevoke] = useState<{ user: User; server: Server } | null>(null);
  const [disableError, setDisableError] = useState("");
  const [demotionError, setDemotionError] = useState("");
  const [revokeError, setRevokeError] = useState("");

  const selectedUser = users.find((user) => user.id === selectedUserId) ?? null;
  const selectedServer = servers.find((server) => server.id === selectedServerId) ?? null;
  const isGlobalAdmin = selectedUser?.roles.includes("platform_admin") ?? false;
  const isCurrentUser = selectedUser?.id === session.user.id;
  const selectedMembershipScope = selectedUser && selectedServer && !isGlobalAdmin ? `${selectedUser.id}:${selectedServer.id}` : "";
  const selectedMembershipScopeRef = useRef(selectedMembershipScope);
  const selectedUserIdRef = useRef(selectedUserId);
  const pendingDisableRef = useRef(pendingDisable);
  const pendingDemotionRef = useRef(pendingDemotion);
  const pendingRevokeRef = useRef(pendingRevoke);
  selectedMembershipScopeRef.current = selectedMembershipScope;
  selectedUserIdRef.current = selectedUserId;
  pendingDisableRef.current = pendingDisable;
  pendingDemotionRef.current = pendingDemotion;
  pendingRevokeRef.current = pendingRevoke;
  const membershipReady = selectedMembershipScope !== "" && membershipScope === selectedMembershipScope && !membershipLoading && !membershipError;
  const visibleUsers = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return users;
    return users.filter((user) => `${user.displayName} ${user.email} ${user.roles.join(" ")}`.toLowerCase().includes(normalized));
  }, [query, users]);

  const load = () => {
    setLoading(true);
    Promise.all([api.users(), api.servers()]).then(([nextUsers, nextServers]) => {
      setUsers(nextUsers);
      setServers(nextServers);
      setSelectedUserId((current) => current || nextUsers[0]?.id || "");
      setSelectedServerId((current) => current || nextServers[0]?.id || "");
      setError("");
    }).catch((reason) => setError(messageFor(reason, copy.feedback.unableLoadUsers))).finally(() => setLoading(false));
  };

  useEffect(() => { load(); }, []);

  useEffect(() => {
    if (pendingDisable && pendingDisable.id !== selectedUserId) {
      setPendingDisable(null);
      setDisableError("");
    }
    if (pendingDemotion && pendingDemotion.id !== selectedUserId) {
      setPendingDemotion(null);
      setDemotionError("");
    }
    if (pendingRevoke && (pendingRevoke.user.id !== selectedUserId || pendingRevoke.server.id !== selectedServerId)) {
      setPendingRevoke(null);
      setRevokeError("");
    }
  }, [pendingDemotion, pendingDisable, pendingRevoke, selectedServerId, selectedUserId]);

  useEffect(() => {
    if (!selectedUser || !selectedServer || isGlobalAdmin) {
      setMembership(null);
      setMembershipScope("");
      setPermissions(["servers.read"]);
      setMembershipError("");
      return;
    }
    let active = true;
    setMembership(null);
    setMembershipScope("");
    setPermissions(["servers.read"]);
    setMembershipLoading(true);
    setMembershipError("");
    api.serverMembership(selectedServer.id, selectedUser.id).then((nextMembership) => {
      if (!active) return;
      if (nextMembership.serverId !== selectedServer.id || nextMembership.userId !== selectedUser.id) {
        setMembershipError(copy.feedback.selectedMembershipMismatch);
        return;
      }
      setMembership(nextMembership);
      setMembershipScope(`${selectedUser.id}:${selectedServer.id}`);
      setPermissions(nextMembership.permissions);
    }).catch((reason) => {
      if (!active) return;
      if (reason instanceof ApiError && reason.code === "NOT_FOUND") {
        setMembership(null);
        setMembershipScope(`${selectedUser.id}:${selectedServer.id}`);
        setPermissions(["servers.read"]);
      } else {
        setMembershipError(messageFor(reason, copy.feedback.unableReadAccess));
      }
    }).finally(() => { if (active) setMembershipLoading(false); });
    return () => { active = false; };
  }, [isGlobalAdmin, selectedServer, selectedUser]);

  const replaceUser = (nextUser: User) => setUsers((current) => current.map((user) => user.id === nextUser.id ? nextUser : user));

  const updateStatus = async (targetUser: User | null): Promise<MutationResult> => {
    if (!targetUser) return { ok: false, message: copy.feedback.noUserSelected };
    setBusyAction("status");
    try {
      const next = await api.updateUser(targetUser.id, { status: targetUser.status === "active" ? "disabled" : "active" }, session.csrfToken);
      replaceUser(next);
      toast(next.status === "active" ? copy.feedback.userEnabled : copy.feedback.userDisabled, next.status === "active" ? "success" : "warning");
      return { ok: true };
    } catch (reason) {
      const message = messageFor(reason, copy.feedback.unableUpdateStatus);
      toast(message, "danger");
      return { ok: false, message };
    } finally {
      setBusyAction("");
    }
  };

  const confirmDisable = async () => {
    const targetUser = pendingDisable;
    if (!targetUser) return;
    setDisableError("");
    const result = await updateStatus(targetUser);
    if (result.ok) {
      setPendingDisable(null);
      setDisableError("");
    } else if ("message" in result && selectedUserIdRef.current === targetUser.id && pendingDisableRef.current?.id === targetUser.id) {
      setDisableError(result.message);
    }
  };

  const replaceRole = async (targetUser: User, role: GlobalRole): Promise<MutationResult> => {
    setBusyAction("role");
    try {
      const next = await api.updateUser(targetUser.id, { roles: [role] }, session.csrfToken);
      replaceUser(next);
      toast(copy.feedback.roleUpdated);
      return { ok: true };
    } catch (reason) {
      const message = messageFor(reason, copy.feedback.unableUpdateRole);
      toast(message, "danger");
      return { ok: false, message };
    } finally {
      setBusyAction("");
    }
  };

  const requestRole = (role: GlobalRole) => {
    if (!selectedUser || isCurrentUser || selectedUser.roles.includes(role)) return;
    if (role === "server_owner" && isGlobalAdmin) {
      setDemotionError("");
      setPendingDemotion(selectedUser);
      return;
    }
    void replaceRole(selectedUser, role);
  };

  const confirmDemotion = async () => {
    const targetUser = pendingDemotion;
    if (!targetUser) return;
    setDemotionError("");
    const result = await replaceRole(targetUser, "server_owner");
    if (result.ok) {
      setPendingDemotion(null);
      setDemotionError("");
    } else if ("message" in result && selectedUserIdRef.current === targetUser.id && pendingDemotionRef.current?.id === targetUser.id) {
      setDemotionError(result.message);
    }
  };

  const issueReset = async () => {
    if (!selectedUser) return;
    setBusyAction("reset");
    try {
      const token = await api.issuePasswordResetToken(selectedUser.id, session.csrfToken);
      setIssuedToken({ ...token, user: selectedUser });
    } catch (reason) {
      toast(messageFor(reason, copy.feedback.unableIssueReset), "danger");
    } finally {
      setBusyAction("");
    }
  };

  const togglePermission = (permission: ServerPermission) => {
    if (permission === "servers.read") return;
    setPermissions((current) => current.includes(permission) ? current.filter((item) => item !== permission) : [...current, permission]);
  };

  const saveMembership = async () => {
    if (!selectedUser || !selectedServer || !membershipReady) return;
    const targetUser = selectedUser;
    const targetServer = selectedServer;
    const targetScope = selectedMembershipScope;
    const targetPermissions = [...permissions];
    setBusyAction("membership");
    try {
      const next = await api.putServerMembership(targetServer.id, targetUser.id, targetPermissions, session.csrfToken);
      if (selectedMembershipScopeRef.current !== targetScope) return;
      if (next.serverId !== targetServer.id || next.userId !== targetUser.id) {
        setMembershipError(copy.feedback.submittedMembershipMismatch);
        return;
      }
      setMembership(next);
      setPermissions(next.permissions);
      setMembershipError("");
      toast(copy.feedback.accessSaved);
    } catch (reason) {
      if (selectedMembershipScopeRef.current === targetScope) {
        setMembershipError(messageFor(reason, copy.feedback.unableSaveAccess));
      }
    } finally {
      setBusyAction("");
    }
  };

  const requestRevokeMembership = () => {
    if (!selectedUser || !selectedServer || !membership || !membershipReady) return;
    setRevokeError("");
    setPendingRevoke({ user: selectedUser, server: selectedServer });
  };

  const revokeMembership = async (targetUser: User, targetServer: Server): Promise<MutationResult> => {
    const targetScope = `${targetUser.id}:${targetServer.id}`;
    setBusyAction("membership");
    try {
      await api.deleteServerMembership(targetServer.id, targetUser.id, session.csrfToken);
      if (selectedMembershipScopeRef.current === targetScope) {
        setMembership(null);
        setPermissions(["servers.read"]);
        setMembershipError("");
      }
      toast(copy.feedback.accessRevoked, "warning");
      return { ok: true };
    } catch (reason) {
      const message = messageFor(reason, copy.feedback.unableRevokeAccess);
      if (selectedMembershipScopeRef.current === targetScope) {
        setMembershipError(message);
      }
      return { ok: false, message };
    } finally {
      setBusyAction("");
    }
  };

  const confirmRevokeMembership = async () => {
    const target = pendingRevoke;
    if (!target) return;
    const targetScope = `${target.user.id}:${target.server.id}`;
    setRevokeError("");
    const result = await revokeMembership(target.user, target.server);
    if (result.ok) {
      setPendingRevoke(null);
      setRevokeError("");
    } else if (
      "message" in result
      && selectedMembershipScopeRef.current === targetScope
      && pendingRevokeRef.current?.user.id === target.user.id
      && pendingRevokeRef.current.server.id === target.server.id
    ) {
      setRevokeError(result.message);
    }
  };

  const closeDisableConfirmation = () => {
    setPendingDisable(null);
    setDisableError("");
  };

  const closeDemotionConfirmation = () => {
    setPendingDemotion(null);
    setDemotionError("");
  };

  const closeRevokeConfirmation = () => {
    setPendingRevoke(null);
    setRevokeError("");
  };

  const onCreated = (user: User) => {
    setUsers((current) => [...current, user]);
    setSelectedUserId(user.id);
    setCreateOpen(false);
    toast(copy.feedback.userCreated);
  };

  if (loading && !users.length) return <section className="page"><LoadingState label={copy.page.loading} /></section>;
  if (error && !users.length) return <section className="page"><ErrorState message={error} onRetry={load} /></section>;

  return (
    <section className="page users-page">
      <div className="page-heading"><div><h1>{copy.page.title}</h1><p className="lede">{copy.page.description}</p></div><button className="button primary" onClick={() => setCreateOpen(true)}><Plus size={17} />{copy.page.newUser}</button></div>
      <div className="identity-summary-row" aria-label={copy.summary.aria}><div><UsersRound size={17} /><strong>{users.length}</strong><span>{copy.summary.localUsers}</span></div><div><span className="stamp-dot mint" /><strong>{users.filter((user) => user.status === "active").length}</strong><span>{copy.summary.active}</span></div><div><ShieldCheck size={17} /><strong>{users.filter((user) => user.roles.includes("platform_admin")).length}</strong><span>{copy.summary.admins}</span></div></div>
      <div className="identity-admin-grid">
        <section className="panel user-directory-panel" aria-label={copy.directory.title}>
          <header className="identity-panel-head"><div><p className="eyebrow">{copy.directory.eyebrow}</p><h2>{copy.directory.title}</h2></div><label className="search-input compact-search"><Search size={15} /><input aria-label={copy.directory.searchAria} value={query} onChange={(event) => setQuery(event.target.value)} placeholder={copy.directory.searchPlaceholder} /></label></header>
          <div className="user-directory-list">{visibleUsers.map((user) => <button key={user.id} className={`user-directory-row${selectedUserId === user.id ? " selected" : ""}`} onClick={() => setSelectedUserId(user.id)} aria-pressed={selectedUserId === user.id} disabled={busyAction === "membership"}><span className="user-directory-avatar" translate="no">{user.displayName.slice(0, 1).toUpperCase()}</span><span className="user-directory-copy" translate="no"><strong>{user.displayName}</strong><small>{user.email}</small></span><span className="user-state"><span className={`user-state-dot ${user.status}`} aria-hidden="true" /><span className="sr-only">{copy.directory.status(copy.statuses[user.status])}</span></span><span className="user-role-code">{user.roles.includes("platform_admin") ? copy.roleCodes.platform_admin : copy.roleCodes.server_owner}</span></button>)}</div>
          {!visibleUsers.length && <div className="empty-state"><UserRound size={24} /><strong>{copy.directory.empty}</strong></div>}
        </section>

        <section className="panel identity-detail-panel" aria-label={copy.detail.aria}>
          {selectedUser ? <>
            <header className="identity-panel-head user-detail-head"><div className="user-detail-title"><span className="user-directory-avatar large" translate="no">{selectedUser.displayName.slice(0, 1).toUpperCase()}</span><div><p className="eyebrow">{copy.detail.selectedEyebrow}</p><h2 translate="no">{selectedUser.displayName}</h2><span translate="no">{selectedUser.email}</span></div></div><span className={`status-badge ${selectedUser.status === "active" ? "status-success" : "status-danger"}`}>{copy.statuses[selectedUser.status]}</span></header>
            <div className="identity-detail-actions"><button className="button secondary" onClick={issueReset} disabled={busyAction !== "" || selectedUser.status !== "active"}><KeyRound size={15} />{copy.detail.issueReset}</button><button className={`button ${selectedUser.status === "active" ? "secondary" : "primary"}`} onClick={() => { if (selectedUser.status === "active") { setDisableError(""); setPendingDisable(selectedUser); } else { void updateStatus(selectedUser); } }} disabled={busyAction !== "" || selectedUser.id === session.user.id}>{selectedUser.status === "active" ? <ShieldOff size={15} /> : <ShieldCheck size={15} />}{selectedUser.status === "active" ? copy.detail.disable : copy.detail.enable}</button></div>
            <div className="identity-detail-section"><div className="section-title-row"><div><p className="eyebrow">{copy.detail.globalRoleEyebrow}</p><h3>{copy.detail.platformScope}</h3></div></div><div className="role-selector" role="group" aria-label={copy.detail.globalRoleAria}><button className={selectedUser.roles.includes("server_owner") && !isGlobalAdmin ? "active" : ""} aria-pressed={selectedUser.roles.includes("server_owner") && !isGlobalAdmin} onClick={() => requestRole("server_owner")} disabled={busyAction !== "" || isCurrentUser}>{copy.roles.server_owner}</button><button className={isGlobalAdmin ? "active" : ""} aria-pressed={isGlobalAdmin} onClick={() => requestRole("platform_admin")} disabled={busyAction !== "" || isCurrentUser}>{copy.roles.platform_admin}</button></div>{isCurrentUser && <p className="field-hint">{copy.detail.ownRoleHint}</p>}</div>
            <div className="identity-detail-section membership-section"><div className="section-title-row"><div><p className="eyebrow">{copy.membership.eyebrow}</p><h3>{copy.membership.title}</h3></div>{membershipLoading && <RefreshCw className="spin" size={15} />}</div>
              {isGlobalAdmin ? <div className="identity-notice"><ShieldCheck size={17} /><span><strong>{copy.membership.globalAccess}</strong><small>{copy.membership.globalDescription}</small></span></div> : servers.length ? <>
                <label className="membership-server-field">{copy.membership.server}<select aria-label={copy.membership.serverAria} value={selectedServerId} onChange={(event) => setSelectedServerId(event.target.value)} disabled={busyAction === "membership"}>{servers.map((server) => <option key={server.id} value={server.id} translate="no">{server.name} / {server.gameName}</option>)}</select></label>
                <div className="permission-grid">{permissionOptions.map((option) => <label key={option.value} className={`permission-option${permissions.includes(option.value) ? " checked" : ""}`}><input type="checkbox" checked={permissions.includes(option.value)} disabled={option.value === "servers.read" || !membershipReady || busyAction !== ""} onChange={() => togglePermission(option.value)} /><span className="permission-check">{permissions.includes(option.value) && <Check size={13} />}</span><span><strong>{copy.permissions[option.value]}</strong><small>{copy.permissionGroups[option.group]}</small></span></label>)}</div>
                {membershipError && <div className="form-error" role="alert">{membershipError}</div>}
                <div className="membership-actions"><button className="button primary" onClick={saveMembership} disabled={!membershipReady || busyAction !== ""}>{copy.membership.save}</button>{membership && membershipReady && <button className="button secondary" onClick={requestRevokeMembership} disabled={busyAction !== ""}>{copy.membership.revoke}</button>}<span translate="no">{selectedServer?.name}</span></div>
              </> : <div className="identity-notice muted"><UsersRound size={17} /><span><strong>{copy.membership.noServers}</strong><small>{copy.membership.noServersDescription}</small></span></div>}
            </div>
          </> : <div className="empty-state"><UserRound size={25} /><strong>{copy.detail.selectUser}</strong></div>}
        </section>
      </div>
      {error && <div className="inline-warning">{error}</div>}
      <Modal
        open={Boolean(pendingDisable)}
        title={copy.disableDialog.title}
        description={pendingDisable ? copy.disableDialog.description(pendingDisable.email) : ""}
        onClose={closeDisableConfirmation}
        dismissible={busyAction === ""}
        footer={<><button className="button secondary" type="button" onClick={closeDisableConfirmation} disabled={busyAction !== ""}>{copy.common.cancel}</button><button className="button danger-solid" type="button" onClick={confirmDisable} disabled={busyAction !== ""}>{busyAction === "status" ? copy.disableDialog.busy : copy.disableDialog.confirm}</button></>}
      >
        {pendingDisable && <div className="modal-form"><div className="danger-confirm"><ShieldOff size={18} /><div translate="no"><strong>{pendingDisable.displayName}</strong><span>{pendingDisable.email}</span></div></div>{disableError && <div className="form-error" role="alert">{disableError}</div>}</div>}
      </Modal>
      <Modal
        open={Boolean(pendingDemotion)}
        title={copy.demotionDialog.title}
        description={copy.demotionDialog.description}
        onClose={closeDemotionConfirmation}
        dismissible={busyAction === ""}
        footer={<><button className="button secondary" type="button" onClick={closeDemotionConfirmation} disabled={busyAction !== ""}>{copy.common.cancel}</button><button className="button danger-solid" type="button" onClick={confirmDemotion} disabled={busyAction !== ""}>{busyAction === "role" ? copy.demotionDialog.busy : copy.demotionDialog.confirm}</button></>}
      >
        {pendingDemotion && <div className="modal-form"><div className="danger-confirm"><ShieldOff size={18} /><div translate="no"><strong>{pendingDemotion.displayName}</strong><span>{pendingDemotion.email}</span></div></div>{demotionError && <div className="form-error" role="alert">{demotionError}</div>}</div>}
      </Modal>
      <Modal
        open={Boolean(pendingRevoke)}
        title={copy.revokeDialog.title}
        description={copy.revokeDialog.description}
        onClose={closeRevokeConfirmation}
        dismissible={busyAction === ""}
        footer={<><button className="button secondary" type="button" onClick={closeRevokeConfirmation} disabled={busyAction !== ""}>{copy.common.cancel}</button><button className="button danger-solid" type="button" onClick={confirmRevokeMembership} disabled={busyAction !== ""}>{busyAction === "membership" ? copy.revokeDialog.busy : copy.revokeDialog.confirm}</button></>}
      >
        {pendingRevoke && <div className="modal-form"><div className="danger-confirm"><ShieldOff size={18} /><div translate="no"><strong>{pendingRevoke.user.email}</strong><span>{pendingRevoke.server.name}</span></div></div>{revokeError && <div className="form-error" role="alert">{revokeError}</div>}</div>}
      </Modal>
      <CreateUserModal open={createOpen} csrfToken={session.csrfToken} onClose={() => setCreateOpen(false)} onCreated={onCreated} copy={copy} />
      <ResetTokenModal issued={issuedToken} onClose={() => setIssuedToken(null)} toast={toast} copy={copy} />
    </section>
  );
}

function CreateUserModal({ open, csrfToken, onClose, onCreated, copy }: { open: boolean; csrfToken: string; onClose: () => void; onCreated: (user: User) => void; copy: UsersCopy }) {
  const [input, setInput] = useState<CreateUserInput>({ email: "", displayName: "", password: "", roles: ["server_owner"] });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const user = await api.createUser(input, csrfToken);
      onCreated(user);
      setInput({ email: "", displayName: "", password: "", roles: ["server_owner"] });
    } catch (reason) {
      setError(messageFor(reason, copy.createDialog.unableCreate));
    } finally {
      setBusy(false);
    }
  };
  return <Modal open={open} title={copy.createDialog.title} description={copy.createDialog.description} onClose={onClose} dismissible={!busy} footer={<><button className="button secondary" type="button" onClick={onClose} disabled={busy}>{copy.common.cancel}</button><button className="button primary" type="submit" form="create-user-form" disabled={busy}>{busy ? copy.createDialog.busy : copy.createDialog.confirm}</button></>}><form id="create-user-form" className="modal-form" onSubmit={submit}><label>{copy.createDialog.email}<input aria-label={copy.createDialog.email} type="email" autoComplete="off" maxLength={254} value={input.email} onChange={(event) => setInput({ ...input, email: event.target.value })} required /></label><label>{copy.createDialog.displayName}<input aria-label={copy.createDialog.displayName} maxLength={64} value={input.displayName} onChange={(event) => setInput({ ...input, displayName: event.target.value })} required /></label><label>{copy.createDialog.temporaryPassword}<input aria-label={copy.createDialog.temporaryPassword} type="password" autoComplete="new-password" minLength={8} maxLength={1024} value={input.password} onChange={(event) => setInput({ ...input, password: event.target.value })} required /></label><label>{copy.createDialog.role}<select aria-label={copy.createDialog.role} value={input.roles[0]} onChange={(event) => setInput({ ...input, roles: [event.target.value as GlobalRole] })}><option value="server_owner">{copy.roles.server_owner}</option><option value="platform_admin">{copy.roles.platform_admin}</option></select></label>{error && <div className="form-error" role="alert">{error}</div>}</form></Modal>;
}

function ResetTokenModal({ issued, onClose, toast, copy }: { issued: IssuedResetToken | null; onClose: () => void; toast: (message: string, tone?: "success" | "warning" | "danger") => void; copy: UsersCopy }) {
  const { locale } = useI18n();
  const copyToken = async () => {
    if (!issued) return;
    try {
      await navigator.clipboard.writeText(issued.token);
      toast(copy.resetDialog.copied);
    } catch {
      toast(copy.resetDialog.clipboardUnavailable, "warning");
    }
  };
  return <Modal open={Boolean(issued)} title={copy.resetDialog.title} description={issued ? copy.resetDialog.description(issued.user.email) : ""} onClose={onClose} footer={<><button className="button secondary" onClick={copyToken}><Clipboard size={15} />{copy.resetDialog.copy}</button><button className="button primary" onClick={onClose}>{copy.resetDialog.done}</button></>}>{issued && <div className="reset-token-sheet"><span>{copy.resetDialog.token}</span><code translate="no">{issued.token}</code><dl><div><dt>{copy.resetDialog.user}</dt><dd translate="no">{issued.user.displayName}</dd></div><div><dt>{copy.resetDialog.expires}</dt><dd>{new Date(issued.expiresAt).toLocaleString(usersIntlLocales[locale])}</dd></div></dl></div>}</Modal>;
}

function messageFor(reason: unknown, fallback: string): string {
  return reason instanceof ApiError ? reason.message : reason instanceof Error ? reason.message : fallback;
}
