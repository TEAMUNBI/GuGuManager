import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { BrowserRouter, Link, NavLink, Navigate, Outlet, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { Cpu, Gamepad2, LayoutDashboard, ListTodo, LogOut, Menu, Network, PanelLeftClose, PanelLeftOpen, ServerCog, Settings, ShieldCheck, UsersRound, X } from "lucide-react";
import { api, ApiError } from "../lib/api";
import type { Session } from "../lib/types";
import { ErrorState, LoadingState } from "../components/PageState";
import { LoginPage } from "../pages/LoginPage";
import { OverviewPage } from "../pages/OverviewPage";
import { ServersPage } from "../pages/ServersPage";
import { ServerWorkspace } from "../pages/ServerWorkspace";
import { NodesPage } from "../pages/NodesPage";
import { GamesPage } from "../pages/GamesPage";
import { AuditPage } from "../pages/AuditPage";
import { SetupPage } from "../pages/SetupPage";
import { ResetPasswordPage } from "../pages/ResetPasswordPage";
import { UsersPage } from "../pages/UsersPage";
import { OperationsPage } from "../pages/OperationsPage";
import { I18nProvider, LanguageSwitcher, type LocalizedCopy, useCopy } from "../i18n/I18n";

interface ShellCopy {
  skipToContent: string;
  home: string;
  closeNavigation: string;
  openNavigation: string;
  expandNavigation: string;
  collapseNavigation: string;
  environment: string;
  primaryNavigation: string;
  controlRoom: string;
  overview: string;
  servers: string;
  operations: string;
  nodes: string;
  catalog: string;
  gameLibrary: string;
  audit: string;
  system: string;
  users: string;
  settings: string;
  workspace: string;
  platformAdmin: string;
  serverOwner: string;
  logout: string;
}

const shellCopy: LocalizedCopy<ShellCopy> = {
  "zh-CN": { skipToContent: "跳到主要内容", home: "GuGuManager 首页", closeNavigation: "关闭导航", openNavigation: "打开导航", expandNavigation: "展开导航", collapseNavigation: "收起导航", environment: "开发环境", primaryNavigation: "主导航", controlRoom: "运维", overview: "运行总览", servers: "服务器", operations: "任务队列", nodes: "节点", catalog: "基础设施", gameLibrary: "游戏模板", audit: "审计日志", system: "管理", users: "用户与访问", settings: "系统状态", workspace: "服务器工作区", platformAdmin: "平台管理员", serverOwner: "服务器所有者", logout: "退出登录" },
  en: { skipToContent: "Skip to main content", home: "GuGuManager home", closeNavigation: "Close navigation", openNavigation: "Open navigation", expandNavigation: "Expand navigation", collapseNavigation: "Collapse navigation", environment: "Development environment", primaryNavigation: "Primary navigation", controlRoom: "OPERATIONS", overview: "Operations overview", servers: "Servers", operations: "Task queue", nodes: "Nodes", catalog: "INFRASTRUCTURE", gameLibrary: "Game templates", audit: "Audit log", system: "ADMIN", users: "Users & access", settings: "System status", workspace: "Server workspace", platformAdmin: "Platform admin", serverOwner: "Server owner", logout: "Sign out" },
  ja: { skipToContent: "メインコンテンツへ移動", home: "GuGuManager ホーム", closeNavigation: "ナビゲーションを閉じる", openNavigation: "ナビゲーションを開く", expandNavigation: "ナビゲーションを展開", collapseNavigation: "ナビゲーションを折りたたむ", environment: "開発環境", primaryNavigation: "メインナビゲーション", controlRoom: "運用", overview: "運用概要", servers: "サーバー", operations: "タスクキュー", nodes: "ノード", catalog: "インフラ", gameLibrary: "ゲームテンプレート", audit: "監査ログ", system: "管理", users: "ユーザーとアクセス", settings: "システム状態", workspace: "サーバーワークスペース", platformAdmin: "プラットフォーム管理者", serverOwner: "サーバー所有者", logout: "サインアウト" },
  ko: { skipToContent: "주요 콘텐츠로 이동", home: "GuGuManager 홈", closeNavigation: "탐색 닫기", openNavigation: "탐색 열기", expandNavigation: "탐색 펼치기", collapseNavigation: "탐색 접기", environment: "개발 환경", primaryNavigation: "기본 탐색", controlRoom: "운영", overview: "운영 개요", servers: "서버", operations: "작업 대기열", nodes: "노드", catalog: "인프라", gameLibrary: "게임 템플릿", audit: "감사 로그", system: "관리", users: "사용자 및 접근", settings: "시스템 상태", workspace: "서버 작업 공간", platformAdmin: "플랫폼 관리자", serverOwner: "서버 소유자", logout: "로그아웃" },
};

const bootstrapCopy: LocalizedCopy<{ connecting: string; inspectSession: string; inspectSetup: string; unavailable: string }> = {
  "zh-CN": { connecting: "正在连接管理服务", inspectSession: "无法读取当前登录状态。", inspectSetup: "无法读取管理服务的初始化状态。", unavailable: "无法连接管理服务。" },
  en: { connecting: "Connecting to the control plane", inspectSession: "Unable to inspect the current session.", inspectSetup: "Unable to inspect control-plane setup state.", unavailable: "Unable to connect to the control plane." },
  ja: { connecting: "コントロールプレーンに接続中", inspectSession: "現在のセッションを確認できません。", inspectSetup: "初期設定の状態を確認できません。", unavailable: "コントロールプレーンに接続できません。" },
  ko: { connecting: "컨트롤 플레인에 연결 중", inspectSession: "현재 세션을 확인할 수 없습니다.", inspectSetup: "초기 설정 상태를 확인할 수 없습니다.", unavailable: "컨트롤 플레인에 연결할 수 없습니다." },
};

interface SettingsCopy {
  eyebrow: string;
  title: string;
  description: string;
  currentSession: string;
  identity: string;
  displayName: string;
  email: string;
  role: string;
  environment: string;
  adapter: string;
  memory: string;
  adapterDescription: string;
  apiActive: string;
  apiDetail: string;
  fallbackActive: string;
  fallbackDetail: string;
}

const settingsCopy: LocalizedCopy<SettingsCopy> = {
  "zh-CN": { eyebrow: "管理 / 系统状态", title: "系统状态", description: "查看当前登录信息、数据适配器和 API 接入状态。", currentSession: "当前登录", identity: "登录账号", displayName: "显示名称", email: "邮箱地址", role: "权限角色", environment: "运行环境", adapter: "数据适配器", memory: "进程内开发模式", adapterDescription: "当前使用进程内数据适配器；PostgreSQL、Agent mTLS 与 OCI 运行时仍在接入中。", apiActive: "API 接口已启用", apiDetail: "REST v1 / 幂等请求 / 审计日志", fallbackActive: "本地模拟数据已启用", fallbackDetail: "后端不可用时仍可预览和检查界面" },
  en: { eyebrow: "ADMIN / SYSTEM STATUS", title: "System status", description: "Review the current session, data adapter, and API integration status.", currentSession: "CURRENT SESSION", identity: "Signed-in account", displayName: "Display name", email: "Email", role: "Role", environment: "Environment", adapter: "DATA ADAPTER", memory: "In-memory development mode", adapterDescription: "This workspace uses an in-process adapter while PostgreSQL, Agent mTLS, and OCI runtime integrations are in progress.", apiActive: "API contract active", apiDetail: "REST v1 / idempotency / audit log", fallbackActive: "Local fallback active", fallbackDetail: "Mock data keeps the interface available when the backend is offline" },
  ja: { eyebrow: "管理 / システム状態", title: "システム状態", description: "現在のセッション、データアダプター、API 接続状態を確認します。", currentSession: "現在のセッション", identity: "サインイン中のアカウント", displayName: "表示名", email: "メール", role: "ロール", environment: "環境", adapter: "データアダプター", memory: "インメモリ開発モード", adapterDescription: "PostgreSQL、Agent mTLS、OCI ランタイムの統合中は、プロセス内アダプターを使用します。", apiActive: "API 契約は有効", apiDetail: "REST v1 / 冪等性 / 監査ログ", fallbackActive: "ローカルフォールバックは有効", fallbackDetail: "バックエンド停止時もモックデータで画面を確認できます" },
  ko: { eyebrow: "관리 / 시스템 상태", title: "시스템 상태", description: "현재 세션, 데이터 어댑터 및 API 연결 상태를 확인합니다.", currentSession: "현재 세션", identity: "로그인 계정", displayName: "표시 이름", email: "이메일", role: "역할", environment: "환경", adapter: "데이터 어댑터", memory: "인메모리 개발 모드", adapterDescription: "PostgreSQL, Agent mTLS 및 OCI 런타임을 통합하는 동안 프로세스 내 어댑터를 사용합니다.", apiActive: "API 계약 활성", apiDetail: "REST v1 / 멱등성 / 감사 로그", fallbackActive: "로컬 대체 경로 활성", fallbackDetail: "백엔드가 오프라인이어도 모의 데이터로 화면을 확인할 수 있습니다" },
};

interface AppContextValue {
  session: Session;
  toast: (message: string, tone?: "success" | "warning" | "danger") => void;
}

const AppContext = createContext<AppContextValue | null>(null);

export function useAppContext() {
  const value = useContext(AppContext);
  if (!value) throw new Error("useAppContext must be used inside the application shell");
  return value;
}

export function App() {
  return <I18nProvider><FluidBackdrop /><AppRouter /></I18nProvider>;
}

function FluidBackdrop() {
  return <div className="fluid-backdrop" aria-hidden="true">
    <span className="fluid-current fluid-current-a" />
    <span className="fluid-current fluid-current-b" />
    <span className="fluid-lens" />
    <span className="fluid-grain" />
  </div>;
}

function AppRouter() {
  const copy = useCopy(bootstrapCopy);
  const [session, setSession] = useState<Session | null>(null);
  const [bootstrap, setBootstrap] = useState<{ state: "checking" | "setup" | "ready" | "error"; expiresAt?: string; message?: string }>({ state: "checking" });
  const [retryKey, setRetryKey] = useState(0);
  useEffect(() => {
    let active = true;
    const check = async () => {
      setBootstrap({ state: "checking" });
      try {
        const setup = await api.setupStatus();
        if (!active) return;
        if (setup.required) {
          setSession(null);
          setBootstrap({ state: "setup", expiresAt: setup.bootstrapExpiresAt });
          return;
        }
        try {
          const current = await api.session();
          if (active) setSession(current);
      } catch (reason) {
        if (!active) return;
        if (
          reason instanceof ApiError &&
          reason.status === 401 &&
          reason.code === "AUTH_REQUIRED"
        ) {
          setSession(null);
        } else {
          setBootstrap({
            state: "error",
            message:
              reason instanceof Error
                ? reason.message
                : copy.inspectSession,
          });
          return;
        }
      }
        if (active) setBootstrap({ state: "ready" });
      } catch (reason) {
        if (active) setBootstrap({ state: "error", message: reason instanceof Error ? reason.message : copy.inspectSetup });
      }
    };
    void check();
    return () => { active = false; };
  }, [retryKey]);

  if (bootstrap.state === "checking") return <LoadingState label={copy.connecting} />;
  if (bootstrap.state === "error") return <main className="bootstrap-error"><ErrorState message={bootstrap.message ?? copy.unavailable} onRetry={() => setRetryKey((value) => value + 1)} /></main>;
  return (
    <BrowserRouter>
      {bootstrap.state === "setup" ? <SetupPage expiresAt={bootstrap.expiresAt} onComplete={() => setBootstrap({ state: "ready" })} /> : !session ? <Routes><Route path="reset-password" element={<ResetPasswordPage />} /><Route path="login" element={<LoginPage onLogin={setSession} />} /><Route path="*" element={<Navigate to="/login" replace />} /></Routes> : (
        <Routes>
          <Route element={<AppShell session={session} onLogout={() => setSession(null)} />}>
            <Route index element={session.user.roles.includes("platform_admin") ? <OverviewPage /> : <Navigate to="/servers" replace />} />
            <Route path="servers" element={<ServersPage />} />
            <Route path="servers/:serverId/*" element={<ServerWorkspace />} />
            <Route path="operations" element={<OperationsPage />} />
            <Route path="catalog" element={<GamesPage />} />
            <Route path="settings" element={<SettingsPage />} />
            {session.user.roles.includes("platform_admin") && <>
              <Route path="nodes" element={<NodesPage />} />
              <Route path="audit" element={<AuditPage />} />
              <Route path="users" element={<UsersPage />} />
            </>}
            <Route path="*" element={<Navigate to={session.user.roles.includes("platform_admin") ? "/" : "/servers"} replace />} />
          </Route>
        </Routes>
      )}
    </BrowserRouter>
  );
}

function AppShell({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const copy = useCopy(shellCopy);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [compact, setCompact] = useState(false);
  const [toast, setToast] = useState<{ message: string; tone: string } | null>(null);
  const mobileMenuButtonRef = useRef<HTMLButtonElement>(null);
  const mobileCloseButtonRef = useRef<HTMLButtonElement>(null);
  const mobileSidebarRef = useRef<HTMLElement>(null);
  const mobileSidebarWasOpenRef = useRef(false);
  const navigate = useNavigate();
  const location = useLocation();
  const previousPathRef = useRef(location.pathname);
  const isAdmin = session.user.roles.includes("platform_admin");
  const notify = useCallback((message: string, tone: "success" | "warning" | "danger" = "success") => {
    setToast({ message, tone });
    window.setTimeout(() => setToast(null), 3600);
  }, []);
  const logout = async () => {
    try { await api.logout(session.csrfToken); } finally { onLogout(); navigate("/login"); }
  };
  useEffect(() => {
    if (!sidebarOpen) {
      if (mobileSidebarWasOpenRef.current) {
        mobileSidebarWasOpenRef.current = false;
        mobileMenuButtonRef.current?.focus();
      }
      return;
    }

    mobileSidebarWasOpenRef.current = true;
    const previousDocumentOverflow = document.documentElement.style.overflow;
    const previousBodyOverflow = document.body.style.overflow;
    document.documentElement.style.overflow = "hidden";
    document.body.style.overflow = "hidden";
    mobileCloseButtonRef.current?.focus();

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setSidebarOpen(false);
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = Array.from(mobileSidebarRef.current?.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), select:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) ?? []).filter((element) => element.getClientRects().length > 0);
      const first = focusable.at(0);
      const last = focusable.at(-1);
      if (!first || !last) {
        event.preventDefault();
        return;
      }
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleKeyDown);

    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      document.documentElement.style.overflow = previousDocumentOverflow;
      document.body.style.overflow = previousBodyOverflow;
    };
  }, [sidebarOpen]);
  useEffect(() => {
    if (previousPathRef.current === location.pathname) return;
    previousPathRef.current = location.pathname;
    setSidebarOpen(false);
  }, [location.pathname]);
  const pageName = location.pathname.startsWith("/servers/") ? copy.workspace : location.pathname === "/servers" ? copy.servers : location.pathname === "/operations" ? copy.operations : location.pathname === "/nodes" ? copy.nodes : location.pathname === "/catalog" ? copy.gameLibrary : location.pathname === "/audit" ? copy.audit : location.pathname === "/users" ? copy.users : location.pathname === "/settings" ? copy.settings : copy.overview;
  const context = useMemo(() => ({ session, toast: notify }), [session, notify]);
  return (
    <AppContext.Provider value={context}>
      <div className={`app-shell${compact ? " sidebar-compact" : ""}`}>
        <a className="skip-link" href="#main-content">{copy.skipToContent}</a>
        <div className={`mobile-scrim${sidebarOpen ? " is-open" : ""}`} onClick={() => setSidebarOpen(false)} aria-hidden="true" />
        <aside ref={mobileSidebarRef} id="primary-sidebar" className={`sidebar${sidebarOpen ? " is-open" : ""}`}>
          <div className="brand-row">
            <Link to="/" className="brand-mark" aria-label={copy.home}><span>G</span><i /></Link>
            {!compact && <div className="brand-copy" translate="no"><strong>GuGu</strong><small>MANAGER / CONTROL</small></div>}
            <button ref={mobileCloseButtonRef} className="icon-button sidebar-close" onClick={() => setSidebarOpen(false)} aria-label={copy.closeNavigation}><X size={18} /></button>
          </div>
          {!compact && <div className="environment-strip"><span className="live-dot" />{copy.environment}<span className="env-code" translate="no">DEV</span></div>}
          <nav className="main-nav" aria-label={copy.primaryNavigation}>
            <NavSection label={copy.controlRoom} compact={compact} />
            {isAdmin && <NavItem to="/" icon={<LayoutDashboard size={18} />} label={copy.overview} compact={compact} end />}
            <NavItem to="/servers" icon={<ServerCog size={18} />} label={copy.servers} compact={compact} />
            <NavItem to="/operations" icon={<ListTodo size={18} />} label={copy.operations} compact={compact} />
            <NavSection label={copy.catalog} compact={compact} />
            {isAdmin && <NavItem to="/nodes" icon={<Network size={18} />} label={copy.nodes} compact={compact} />}
            <NavItem to="/catalog" icon={<Gamepad2 size={18} />} label={copy.gameLibrary} compact={compact} />
            <NavSection label={copy.system} compact={compact} />
            {isAdmin && <NavItem to="/users" icon={<UsersRound size={18} />} label={copy.users} compact={compact} />}
            {isAdmin && <NavItem to="/audit" icon={<ShieldCheck size={18} />} label={copy.audit} compact={compact} />}
          </nav>
          <div className="sidebar-bottom">
            <NavItem to="/settings" icon={<Settings size={18} />} label={copy.settings} compact={compact} />
            {!compact && <LanguageSwitcher placement="sidebar" />}
            <button className="profile-row" onClick={logout} title={copy.logout} aria-label={copy.logout}><span className="avatar">{session.user.displayName.slice(0, 1)}</span>{!compact && <span className="profile-copy"><strong>{session.user.displayName}</strong><small>{isAdmin ? copy.platformAdmin : copy.serverOwner}</small></span>}<LogOut size={16} /></button>
          </div>
          <button className="sidebar-toggle" onClick={() => setCompact((value) => !value)} aria-label={compact ? copy.expandNavigation : copy.collapseNavigation} title={compact ? copy.expandNavigation : copy.collapseNavigation}>{compact ? <PanelLeftOpen size={17} /> : <PanelLeftClose size={17} />}</button>
        </aside>
        <main id="main-content" className="main-area" tabIndex={-1} inert={sidebarOpen ? true : undefined} aria-hidden={sidebarOpen ? true : undefined}>
          <header className="topbar">
            <div className="topbar-left"><button ref={mobileMenuButtonRef} className="icon-button mobile-menu" onClick={() => setSidebarOpen(true)} aria-label={copy.openNavigation} aria-controls="primary-sidebar" aria-expanded={sidebarOpen}><Menu size={20} /></button><div className="breadcrumb"><span translate="no">GuGuManager</span><b>/</b><strong>{pageName}</strong></div></div>
            <div className="topbar-actions"><LanguageSwitcher placement="topbar" /><div className="topbar-user"><span className="avatar avatar-small">{session.user.displayName.slice(0, 1)}</span><span>{session.user.displayName}</span></div></div>
          </header>
          <div className="content-area"><Outlet context={context} /></div>
        </main>
        {toast && <div className={`toast toast-${toast.tone}`} role={toast.tone === "danger" ? "alert" : "status"}><span className="toast-dot" />{toast.message}</div>}
      </div>
    </AppContext.Provider>
  );
}

function NavSection({ label, compact }: { label: string; compact: boolean }) { return compact ? <div className="nav-divider" /> : <p className="nav-section-label">{label}</p>; }

function NavItem({ to, icon, label, compact, end = false }: { to: string; icon: React.ReactNode; label: string; compact: boolean; end?: boolean }) {
  return <NavLink to={to} end={end} className={({ isActive }) => `nav-item${isActive ? " active" : ""}`} title={compact ? label : undefined} aria-label={compact ? label : undefined}><span className="nav-icon">{icon}</span>{!compact && <span>{label}</span>}</NavLink>;
}

function SettingsPage() {
  const { session } = useAppContext();
  const copy = useCopy(settingsCopy);
  return <section className="page settings-page"><div className="page-heading"><div><h1>{copy.title}</h1><p className="lede">{copy.description}</p></div></div><div className="settings-grid"><div className="panel settings-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.currentSession}</p><h2>{copy.identity}</h2></div><ShieldCheck size={20} /></div><dl className="detail-list"><div><dt>{copy.displayName}</dt><dd>{session.user.displayName}</dd></div><div><dt>{copy.email}</dt><dd>{session.user.email}</dd></div><div><dt>{copy.role}</dt><dd><span className="code-pill">{session.user.roles.join(" / ")}</span></dd></div><div><dt>{copy.environment}</dt><dd><span className="env-pill">{session.environment}</span></dd></div></dl></div><div className="panel settings-panel"><div className="panel-heading"><div><p className="eyebrow">{copy.adapter}</p><h2>{copy.memory}</h2></div><Cpu size={20} /></div><p className="panel-copy">{copy.adapterDescription}</p><div className="adapter-check"><span className="check-mark">✓</span><span><strong>{copy.apiActive}</strong><small>{copy.apiDetail}</small></span></div><div className="adapter-check"><span className="check-mark">✓</span><span><strong>{copy.fallbackActive}</strong><small>{copy.fallbackDetail}</small></span></div></div></div></section>;
}
