import { FormEvent, useState } from "react";
import { ArrowRight, KeyRound, LockKeyhole, RadioTower, ShieldCheck } from "lucide-react";
import { Link, useNavigate } from "react-router-dom";
import { api, ApiError } from "../lib/api";
import type { Session } from "../lib/types";
import { LanguageSwitcher, type LocalizedCopy, useCopy } from "../i18n/I18n";

interface LoginCopy {
  eyebrow: string;
  headingLead: string;
  headingAccent: string;
  description: string;
  email: string;
  password: string;
  connecting: string;
  submit: string;
  reset: string;
  adapter: string;
  protected: string;
  development: string;
  worldEyebrow: string;
  worldLead: string;
  worldAccent: string;
  worldTail: string;
  worldDescription: string;
  signal: string;
  connectionError: string;
}

const loginCopy: LocalizedCopy<LoginCopy> = {
  "zh-CN": { eyebrow: "GuGuManager / 登录", headingLead: "欢迎回来，", headingAccent: "运维人员。", description: "登录后继续管理游戏服务器。", email: "邮箱地址", password: "密码", connecting: "正在登录…", submit: "登录", reset: "使用重置令牌", adapter: "本地开发环境", protected: "会话安全保护已启用", development: "开发环境", worldEyebrow: "一个面板，管理所有游戏世界", worldLead: "让每个", worldAccent: "游戏世界", worldTail: "稳定运行。", worldDescription: "集中管理节点、游戏模板、后台任务与故障恢复。", signal: "管理服务 / 本地开发模式", connectionError: "无法连接管理服务，请确认服务已经启动。" },
  en: { eyebrow: "CONTROL PLANE / SIGN IN", headingLead: "Welcome back,", headingAccent: "operator.", description: "Sign in to continue managing your game servers.", email: "Email address", password: "Password", connecting: "Signing in...", submit: "Sign in", reset: "Use a reset token", adapter: "Local development", protected: "Protected session", development: "DEVELOPMENT", worldEyebrow: "ONE CONSOLE FOR EVERY WORLD", worldLead: "Keep the", worldAccent: "worlds", worldTail: "running.", worldDescription: "Nodes, game templates, tasks, and recovery in one calm surface.", signal: "Control plane / local adapter", connectionError: "Unable to connect to the control plane. Confirm that it is running." },
  ja: { eyebrow: "コントロールプレーン / サインイン", headingLead: "おかえりなさい、", headingAccent: "オペレーター。", description: "ログインしてゲームサーバーの管理を続けます。", email: "メールアドレス", password: "パスワード", connecting: "ログイン中…", submit: "ログイン", reset: "リセットトークンを使用", adapter: "ローカル開発環境", protected: "セッション保護は有効です", development: "開発環境", worldEyebrow: "すべての世界を一つの画面で", worldLead: "世界を", worldAccent: "いつでも", worldTail: "稼働中に。", worldDescription: "ノード、ゲームテンプレート、タスク、復旧を一つの画面で管理します。", signal: "コントロールプレーン / ローカルアダプター", connectionError: "コントロールプレーンに接続できません。サービスが起動していることを確認してください。" },
  ko: { eyebrow: "컨트롤 플레인 / 로그인", headingLead: "다시 오셨군요,", headingAccent: "운영자님.", description: "로그인하여 게임 서버 관리를 계속하세요.", email: "이메일 주소", password: "비밀번호", connecting: "로그인 중…", submit: "로그인", reset: "재설정 토큰 사용", adapter: "로컬 개발 환경", protected: "세션 보호 활성화", development: "개발 환경", worldEyebrow: "모든 월드를 위한 하나의 콘솔", worldLead: "모든", worldAccent: "월드를", worldTail: "계속 운영하세요.", worldDescription: "노드, 게임 템플릿, 작업과 복구를 하나의 화면에서 관리합니다.", signal: "컨트롤 플레인 / 로컬 어댑터", connectionError: "컨트롤 플레인에 연결할 수 없습니다. 서비스가 실행 중인지 확인하세요." },
};

export function LoginPage({ onLogin }: { onLogin: (session: Session) => void }) {
  const copy = useCopy(loginCopy);
  const navigate = useNavigate();
  const [email, setEmail] = useState("admin@gugu.local");
  const [password, setPassword] = useState("gugu-dev-2026");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const session = await api.login(email, password);
      onLogin(session);
      navigate("/", { replace: true });
    } catch (reason) {
      setError(reason instanceof ApiError ? reason.message : copy.connectionError);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="login-screen">
      <section className="login-panel">
        <div className="login-toolbar"><div className="login-brand" translate="no"><span className="brand-mark"><span>G</span><i /></span><div><strong>GuGu</strong><small>MANAGER / CONTROL</small></div></div><LanguageSwitcher placement="auth" /></div>
        <div className="login-heading"><h1>{copy.headingLead}<br /><em>{copy.headingAccent}</em></h1><p>{copy.description}</p></div>
        <form className="login-form" onSubmit={submit}>
          <label>{copy.email}<div className="input-wrap"><KeyRound size={16} /><input aria-label={copy.email} name="email" autoComplete="username" spellCheck={false} value={email} onChange={(event) => setEmail(event.target.value)} type="email" required /></div></label>
          <label>{copy.password}<div className="input-wrap"><LockKeyhole size={16} /><input aria-label={copy.password} name="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} type="password" required /></div></label>
          {error && <div className="form-error" role="alert">{error}</div>}
          <button className="button primary login-button" type="submit" disabled={busy}>{busy ? copy.connecting : copy.submit}<ArrowRight size={17} /></button>
        </form>
        <div className="auth-link-row"><Link to="/reset-password">{copy.reset}<ArrowRight size={14} /></Link></div>
        <div className="login-foot"><span><RadioTower size={15} />{copy.adapter}</span><span><ShieldCheck size={15} />{copy.protected}</span></div>
      </section>
    </main>
  );
}
