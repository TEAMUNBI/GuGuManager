import { FormEvent, useState } from "react";
import { ArrowRight, Clock3, KeyRound, LockKeyhole, Mail, ShieldCheck, UserRound } from "lucide-react";
import { api, ApiError } from "../lib/api";
import { LanguageSwitcher, type LocalizedCopy, useCopy, useI18n } from "../i18n/I18n";

interface SetupCopy {
  eyebrow: string; headingLead: string; headingAccent: string; description: string;
  bootstrapToken: string; email: string; displayName: string; passphrase: string; confirmPassphrase: string;
  mismatch: string; failure: string; initializing: string; submit: string; localIdentity: string; expires: (time: string) => string;
  setupRequired: string; trust: string; operatorLead: string; operatorAccent: string;
  credential: string; argon: string; adminRole: string; signal: string; window: string;
}

const setupCopy: LocalizedCopy<SetupCopy> = {
  "zh-CN": { eyebrow: "GuGuManager / 初始设置", headingLead: "创建第一位", headingAccent: "平台管理员", description: "设置用于管理此实例的本地管理员账号。", bootstrapToken: "初始化令牌", email: "邮箱地址", displayName: "显示名称", passphrase: "密码", confirmPassphrase: "确认密码", mismatch: "两次输入的密码不一致。", failure: "无法完成管理服务初始化。", initializing: "正在初始化…", submit: "创建管理员", localIdentity: "本地管理员账号", expires: (time) => `有效期至 ${time}`, setupRequired: "需要完成初始化", trust: "初始信任凭据", operatorLead: "一位本地", operatorAccent: "平台管理员。", credential: "初始化令牌", argon: "使用 Argon2id 保护密码", adminRole: "授予平台管理员权限", signal: "管理服务 / 本地开发模式", window: "令牌在 15 分钟内有效" },
  en: { eyebrow: "CONTROL PLANE / INITIAL SETUP", headingLead: "Create the first", headingAccent: "administrator", description: "Set up the local administrator account for this instance.", bootstrapToken: "Bootstrap token", email: "Email", displayName: "Display name", passphrase: "Password", confirmPassphrase: "Confirm password", mismatch: "Passwords do not match.", failure: "Unable to initialize the control plane.", initializing: "Initializing...", submit: "Create administrator", localIdentity: "Local administrator account", expires: (time) => `Valid until ${time}`, setupRequired: "SETUP REQUIRED", trust: "ROOT OF TRUST", operatorLead: "One local", operatorAccent: "operator.", credential: "Bootstrap credential", argon: "Protected with Argon2id", adminRole: "Platform administrator role", signal: "Control plane / local development", window: "Valid for 15 minutes" },
  ja: { eyebrow: "コントロールプレーン / 初期設定", headingLead: "最初の", headingAccent: "管理者を作成", description: "このインスタンスを管理するローカル管理者アカウントを設定します。", bootstrapToken: "ブートストラップトークン", email: "メール", displayName: "表示名", passphrase: "パスワード", confirmPassphrase: "パスワードを確認", mismatch: "パスワードが一致しません。", failure: "コントロールプレーンを初期化できません。", initializing: "初期化中…", submit: "管理者を作成", localIdentity: "ローカル管理者アカウント", expires: (time) => `有効期限：${time}`, setupRequired: "設定が必要", trust: "信頼の起点", operatorLead: "一人のローカル", operatorAccent: "オペレーター。", credential: "ブートストラップ認証情報", argon: "Argon2id でパスワードを保護", adminRole: "プラットフォーム管理者ロール", signal: "コントロールプレーン / ローカル開発", window: "15 分間有効" },
  ko: { eyebrow: "컨트롤 플레인 / 초기 설정", headingLead: "첫 번째", headingAccent: "관리자 만들기", description: "이 인스턴스를 관리할 로컬 관리자 계정을 설정합니다.", bootstrapToken: "부트스트랩 토큰", email: "이메일", displayName: "표시 이름", passphrase: "비밀번호", confirmPassphrase: "비밀번호 확인", mismatch: "비밀번호가 일치하지 않습니다.", failure: "컨트롤 플레인을 초기화할 수 없습니다.", initializing: "초기화 중…", submit: "관리자 만들기", localIdentity: "로컬 관리자 계정", expires: (time) => `${time}까지 유효`, setupRequired: "설정 필요", trust: "신뢰 기준점", operatorLead: "한 명의 로컬", operatorAccent: "운영자.", credential: "부트스트랩 자격 증명", argon: "Argon2id로 비밀번호 보호", adminRole: "플랫폼 관리자 역할", signal: "컨트롤 플레인 / 로컬 개발", window: "15분 동안 유효" },
};

export function SetupPage({ expiresAt, onComplete }: { expiresAt?: string; onComplete: () => void }) {
  const copy = useCopy(setupCopy);
  const { locale } = useI18n();
  const [bootstrapToken, setBootstrapToken] = useState("");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (password !== confirmation) {
      setError(copy.mismatch);
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.setupAdmin({ bootstrapToken, email, displayName, password });
      onComplete();
    } catch (reason) {
      setError(reason instanceof ApiError ? reason.message : copy.failure);
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="login-screen identity-screen">
      <section className="login-panel identity-panel">
        <div className="login-toolbar"><div className="login-brand" translate="no"><span className="brand-mark"><span>G</span><i /></span><div><strong>GuGu</strong><small>MANAGER / CONTROL</small></div></div><LanguageSwitcher placement="auth" /></div>
        <div className="login-heading identity-heading"><h1>{copy.headingLead}<br /><em>{copy.headingAccent}</em></h1><p>{copy.description}</p></div>
        <form className="login-form identity-form" onSubmit={submit}>
          <label>{copy.bootstrapToken}<div className="input-wrap"><KeyRound size={16} /><input aria-label={copy.bootstrapToken} name="bootstrap-token" autoComplete="one-time-code" spellCheck={false} value={bootstrapToken} onChange={(event) => setBootstrapToken(event.target.value)} type="password" minLength={32} maxLength={256} required /></div></label>
          <div className="identity-form-pair">
            <label>{copy.email}<div className="input-wrap"><Mail size={16} /><input aria-label={copy.email} name="email" autoComplete="username" spellCheck={false} value={email} onChange={(event) => setEmail(event.target.value)} type="email" maxLength={254} required /></div></label>
            <label>{copy.displayName}<div className="input-wrap"><UserRound size={16} /><input aria-label={copy.displayName} name="display-name" autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} maxLength={64} required /></div></label>
          </div>
          <div className="identity-form-pair">
            <label>{copy.passphrase}<div className="input-wrap"><LockKeyhole size={16} /><input aria-label={copy.passphrase} name="password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} type="password" minLength={8} maxLength={1024} required /></div></label>
            <label>{copy.confirmPassphrase}<div className="input-wrap"><ShieldCheck size={16} /><input aria-label={copy.confirmPassphrase} name="confirm-password" autoComplete="new-password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} type="password" minLength={8} maxLength={1024} required /></div></label>
          </div>
          {error && <div className="form-error" role="alert">{error}</div>}
          <button className="button primary login-button" type="submit" disabled={busy}>{busy ? copy.initializing : copy.submit}<ArrowRight size={17} /></button>
        </form>
        <div className="login-foot"><span><ShieldCheck size={15} />{copy.localIdentity}</span>{expiresAt && <span><Clock3 size={15} />{copy.expires(new Date(expiresAt).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" }))}</span>}</div>
      </section>
    </main>
  );
}
