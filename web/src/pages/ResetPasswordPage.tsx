import { FormEvent, useState } from "react";
import { ArrowLeft, ArrowRight, KeyRound, LockKeyhole, ShieldCheck } from "lucide-react";
import { Link, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../lib/api";
import { LanguageSwitcher, type LocalizedCopy, useCopy } from "../i18n/I18n";

interface ResetCopy {
  eyebrow: string; headingLead: string; headingAccent: string; description: string;
  token: string; password: string; confirmation: string; mismatch: string; failure: string;
  replacing: string; replace: string; replaced: string; revoked: string; returnToSignIn: string; back: string;
  oneTime: string; rotation: string; asideLead: string; asideAccent: string; signal: string; localUser: string;
}

const resetCopy: LocalizedCopy<ResetCopy> = {
  "zh-CN": { eyebrow: "账户 / 密码恢复", headingLead: "设置新的", headingAccent: "账户密码", description: "使用一次性重置令牌，为此账户设置新密码。", token: "重置令牌", password: "新密码", confirmation: "再次输入新密码", mismatch: "两次输入的密码不一致。", failure: "密码重置失败。", replacing: "正在重置…", replace: "重置密码", replaced: "密码已重置", revoked: "此账户的其他登录会话均已退出。", returnToSignIn: "返回登录", back: "返回登录", oneTime: "一次性令牌", rotation: "安全重置", asideLead: "重置密码，", asideAccent: "重新安全登录。", signal: "重置成功后，旧会话将自动退出", localUser: "本地账户" },
  en: { eyebrow: "ACCOUNT / RECOVERY", headingLead: "Choose a new", headingAccent: "password", description: "Use your one-time reset token to choose a new password for this account.", token: "Reset token", password: "New password", confirmation: "Confirm new password", mismatch: "Passwords do not match.", failure: "Unable to reset the password.", replacing: "Resetting...", replace: "Reset password", replaced: "Password reset", revoked: "All other sessions for this account have been signed out.", returnToSignIn: "Return to sign in", back: "Back to sign in", oneTime: "ONE-TIME TOKEN", rotation: "SECURE RESET", asideLead: "Reset your password.", asideAccent: "Sign in safely.", signal: "Other sessions / signed out after reset", localUser: "LOCAL ACCOUNT" },
  ja: { eyebrow: "アカウント / パスワード再設定", headingLead: "新しい", headingAccent: "パスワードを設定", description: "一度だけ使用できるリセットトークンで、このアカウントのパスワードを再設定します。", token: "リセットトークン", password: "新しいパスワード", confirmation: "新しいパスワードを再入力", mismatch: "入力したパスワードが一致しません。", failure: "パスワードを再設定できませんでした。", replacing: "再設定中…", replace: "パスワードを再設定", replaced: "パスワードを再設定しました", revoked: "このアカウントの他のセッションからログアウトしました。", returnToSignIn: "ログインに戻る", back: "ログインに戻る", oneTime: "一度だけ使用可能", rotation: "安全なパスワード再設定", asideLead: "パスワードを再設定して、", asideAccent: "安全にログイン。", signal: "再設定完了後、以前のセッションからログアウト", localUser: "ローカルアカウント" },
  ko: { eyebrow: "계정 / 비밀번호 재설정", headingLead: "새", headingAccent: "비밀번호 설정", description: "일회용 재설정 토큰으로 이 계정의 비밀번호를 새로 설정합니다.", token: "재설정 토큰", password: "새 비밀번호", confirmation: "새 비밀번호 확인", mismatch: "입력한 비밀번호가 일치하지 않습니다.", failure: "비밀번호를 재설정할 수 없습니다.", replacing: "재설정 중…", replace: "비밀번호 재설정", replaced: "비밀번호를 재설정했습니다", revoked: "이 계정의 다른 로그인 세션에서 모두 로그아웃했습니다.", returnToSignIn: "로그인으로 돌아가기", back: "로그인으로 돌아가기", oneTime: "일회용 토큰", rotation: "안전한 비밀번호 재설정", asideLead: "비밀번호를 재설정하고,", asideAccent: "안전하게 다시 로그인하세요.", signal: "재설정 완료 후 기존 세션에서 로그아웃", localUser: "로컬 계정" },
};

export function ResetPasswordPage() {
  const copy = useCopy(resetCopy);
  const [search] = useSearchParams();
  const [token, setToken] = useState(search.get("token") ?? "");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [complete, setComplete] = useState(false);
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
      await api.resetPassword(token, password);
      setComplete(true);
    } catch (reason) {
      setError(reason instanceof ApiError ? reason.message : copy.failure);
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="login-screen identity-screen reset-screen">
      <section className="login-panel identity-panel">
        <div className="login-toolbar"><div className="login-brand" translate="no"><span className="brand-mark"><span>G</span><i /></span><div><strong>GuGu</strong><small>MANAGER / IDENTITY</small></div></div><LanguageSwitcher placement="auth" /></div>
        <div className="login-heading identity-heading"><h1>{copy.headingLead}<br /><em>{copy.headingAccent}</em></h1><p>{copy.description}</p></div>
        {complete ? <div className="reset-complete" role="status"><ShieldCheck size={27} /><div><strong>{copy.replaced}</strong><span>{copy.revoked}</span></div><Link className="button primary" to="/login">{copy.returnToSignIn}<ArrowRight size={16} /></Link></div> : <form className="login-form identity-form" onSubmit={submit}>
          <label>{copy.token}<div className="input-wrap"><KeyRound size={16} /><input aria-label={copy.token} name="reset-token" autoComplete="one-time-code" spellCheck={false} value={token} onChange={(event) => setToken(event.target.value)} minLength={32} maxLength={256} required /></div></label>
          <label>{copy.password}<div className="input-wrap"><LockKeyhole size={16} /><input aria-label={copy.password} name="new-password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} type="password" minLength={8} maxLength={1024} required /></div></label>
          <label>{copy.confirmation}<div className="input-wrap"><ShieldCheck size={16} /><input aria-label={copy.confirmation} name="confirm-password" autoComplete="new-password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} type="password" minLength={8} maxLength={1024} required /></div></label>
          {error && <div className="form-error" role="alert">{error}</div>}
          <button className="button primary login-button" type="submit" disabled={busy}>{busy ? copy.replacing : copy.replace}<ArrowRight size={17} /></button>
        </form>}
        <div className="auth-link-row"><Link to="/login"><ArrowLeft size={15} />{copy.back}</Link></div>
      </section>
    </main>
  );
}
