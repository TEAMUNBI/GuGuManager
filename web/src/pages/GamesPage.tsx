import { useEffect, useMemo, useState } from "react";
import { Check, CircleAlert, CircleDot, Code2, Gamepad2, PackageX, Search, ShieldAlert, ShieldCheck } from "lucide-react";
import { api } from "../lib/api";
import type { GameDefinition } from "../lib/types";
import { LoadingState, ErrorState } from "../components/PageState";
import { StatusBadge } from "../components/StatusBadge";
import { type Locale, type LocalizedCopy, useCopy, useI18n } from "../i18n/I18n";
import { localizedGameCapability, localizedGameSummary } from "../lib/gamePresentation";

interface GamesCopy {
  page: {
    eyebrow: string;
    title: string;
    description: string;
    loading: string;
    loadError: string;
  };
  integrity: {
    title: string;
    description: string;
  };
  schemaVersion: string;
  filters: {
    aria: string;
    all: string;
    runnable: string;
    unavailable: string;
  };
  search: { aria: string; placeholder: string };
  empty: { title: string; description: string };
  status: { approved: string; pending: string; rejected: string };
  trust: {
    signed: string;
    unsigned: string;
    verified: string;
    unverified: string;
    runnable: string;
    unavailable: string;
    supported: string;
    unsupported: string;
    trustLevel: (level: string) => string;
    source: (source: string) => string;
  };
  reasons: Record<string, string>;
  card: {
    definition: (version: string) => string;
    servers: (count: number) => string;
  };
}

const gamesCopy: LocalizedCopy<GamesCopy> = {
  "zh-CN": {
    page: { eyebrow: "资源库 / 游戏模板", title: "游戏模板", description: "管理可用于创建服务器的游戏版本、启动参数和运行包。", loading: "正在读取游戏模板", loadError: "无法加载游戏模板" },
    integrity: { title: "目录信任状态按证据显示", description: "已批准且具有可运行目标的模板才能用于创建服务器；签名、验证与支持声明会分别显示。当前内置目录尚无可运行目标。" },
    schemaVersion: "结构版本",
    filters: { aria: "按可运行状态筛选游戏模板", all: "全部", runnable: "可运行", unavailable: "不可用" },
    search: { aria: "搜索游戏模板", placeholder: "搜索游戏或版本" },
    empty: { title: "没有符合条件的游戏模板。", description: "请调整搜索内容或可运行状态。" },
    status: { approved: "已批准", pending: "待审核", rejected: "已拒绝" },
    trust: { signed: "已签名", unsigned: "未签名", verified: "已验证", unverified: "未验证", runnable: "可运行", unavailable: "不可运行", supported: "受支持", unsupported: "不受支持", trustLevel: (level) => `信任级别 ${level}`, source: (source) => `来源 ${source}` },
    reasons: { BUNDLE_SIGNATURE_UNVERIFIED: "运行包签名尚未验证", RUNTIME_TARGET_UNAVAILABLE: "没有可用的运行目标" },
    card: { definition: (version) => `模板版本 ${version}`, servers: (count) => `${count} 台服务器正在使用` },
  },
  en: {
    page: { eyebrow: "LIBRARY / GAME TEMPLATES", title: "Game templates", description: "Manage game releases, startup settings, and runtime packages used to create servers.", loading: "Loading game templates", loadError: "Unable to load game templates" },
    integrity: { title: "Catalog trust follows recorded evidence", description: "A template must be approved and runnable to create servers; signing, verification, and support claims are shown separately. The embedded catalog has no runnable target yet." },
    schemaVersion: "Schema",
    filters: { aria: "Filter game templates by runtime availability", all: "All", runnable: "Runnable", unavailable: "Unavailable" },
    search: { aria: "Search game templates", placeholder: "Search game or version" },
    empty: { title: "No game templates match.", description: "Change the search or runtime availability." },
    status: { approved: "Approved", pending: "Pending review", rejected: "Rejected" },
    trust: { signed: "Signed", unsigned: "Unsigned", verified: "Verified", unverified: "Unverified", runnable: "Runnable", unavailable: "Not runnable", supported: "Supported", unsupported: "Unsupported", trustLevel: (level) => `Trust ${level}`, source: (source) => `Source ${source}` },
    reasons: { BUNDLE_SIGNATURE_UNVERIFIED: "Bundle signature has not been verified", RUNTIME_TARGET_UNAVAILABLE: "No runnable target is available" },
    card: { definition: (version) => `Template version ${version}`, servers: (count) => `Used by ${count} ${count === 1 ? "server" : "servers"}` },
  },
  ja: {
    page: { eyebrow: "ライブラリ / ゲームテンプレート", title: "ゲームテンプレート", description: "サーバー作成に使用するゲームバージョン、起動設定、実行パッケージを管理します。", loading: "ゲームテンプレートを読み込み中", loadError: "ゲームテンプレートを読み込めません" },
    integrity: { title: "カタログの信頼状態は証拠に基づいて表示されます", description: "承認済みで実行可能なテンプレートだけがサーバー作成に使用できます。署名、検証、サポートの状態は個別に表示されます。組み込みカタログにはまだ実行可能ターゲットがありません。" },
    schemaVersion: "スキーマ",
    filters: { aria: "実行可否でゲームテンプレートを絞り込む", all: "すべて", runnable: "実行可能", unavailable: "利用不可" },
    search: { aria: "ゲームテンプレートを検索", placeholder: "ゲームまたはバージョンを検索" },
    empty: { title: "条件に一致するゲームテンプレートはありません。", description: "検索語または実行可否を変更してください。" },
    status: { approved: "承認済み", pending: "審査待ち", rejected: "却下" },
    trust: { signed: "署名済み", unsigned: "未署名", verified: "検証済み", unverified: "未検証", runnable: "実行可能", unavailable: "実行不可", supported: "サポート対象", unsupported: "サポート対象外", trustLevel: (level) => `信頼レベル ${level}`, source: (source) => `提供元 ${source}` },
    reasons: { BUNDLE_SIGNATURE_UNVERIFIED: "実行パッケージの署名は未検証です", RUNTIME_TARGET_UNAVAILABLE: "利用可能な実行ターゲットがありません" },
    card: { definition: (version) => `テンプレートバージョン ${version}`, servers: (count) => `${count} 台のサーバーで使用中` },
  },
  ko: {
    page: { eyebrow: "라이브러리 / 게임 템플릿", title: "게임 템플릿", description: "서버 생성에 사용할 게임 버전, 시작 설정과 실행 패키지를 관리합니다.", loading: "게임 템플릿을 불러오는 중", loadError: "게임 템플릿을 불러올 수 없습니다" },
    integrity: { title: "카탈로그 신뢰 상태는 기록된 증거대로 표시됩니다", description: "승인되고 실행 가능한 템플릿만 서버를 만들 수 있습니다. 서명, 검증 및 지원 상태는 별도로 표시됩니다. 내장 카탈로그에는 아직 실행 가능한 대상이 없습니다." },
    schemaVersion: "스키마",
    filters: { aria: "실행 가능 여부별 게임 템플릿 필터", all: "전체", runnable: "실행 가능", unavailable: "사용 불가" },
    search: { aria: "게임 템플릿 검색", placeholder: "게임 또는 버전 검색" },
    empty: { title: "조건과 일치하는 게임 템플릿이 없습니다.", description: "검색어나 실행 가능 여부를 변경해 보세요." },
    status: { approved: "승인됨", pending: "검토 대기", rejected: "거부됨" },
    trust: { signed: "서명됨", unsigned: "서명되지 않음", verified: "검증됨", unverified: "검증되지 않음", runnable: "실행 가능", unavailable: "실행 불가", supported: "지원됨", unsupported: "지원되지 않음", trustLevel: (level) => `신뢰 수준 ${level}`, source: (source) => `출처 ${source}` },
    reasons: { BUNDLE_SIGNATURE_UNVERIFIED: "실행 패키지 서명이 검증되지 않았습니다", RUNTIME_TARGET_UNAVAILABLE: "사용 가능한 실행 대상이 없습니다" },
    card: { definition: (version) => `템플릿 버전 ${version}`, servers: (count) => `서버 ${count}개에서 사용 중` },
  },
};

function isUsableGame(game: GameDefinition) {
  return game.status === "approved" && game.runnable;
}

function isUnavailableGame(game: GameDefinition) {
  return !isUsableGame(game);
}

export function GamesPage() {
  const copy = useCopy(gamesCopy);
  const { locale } = useI18n();
  const [games, setGames] = useState<GameDefinition[]>([]);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<"all" | "runnable" | "unavailable">("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    api.games()
      .then(setGames)
      .catch((reason) => setError(reason instanceof Error ? reason.message : copy.page.loadError))
      .finally(() => setLoading(false));
  }, []);

  const visible = useMemo(
    () => games.filter((game) => {
      const matchesFilter = filter === "all" || (filter === "runnable" ? isUsableGame(game) : isUnavailableGame(game));
      return matchesFilter && `${game.name} ${localizedGameSummary(game, locale)}`.toLowerCase().includes(query.toLowerCase());
    }),
    [filter, games, locale, query],
  );

  if (loading) return <section className="page"><LoadingState label={copy.page.loading} /></section>;
  if (error) return <section className="page"><ErrorState message={error} /></section>;

  return (
    <section className="page games-page">
      <div className="page-heading page-heading-wide"><div><h1>{copy.page.title}</h1><p className="lede">{copy.page.description}</p></div></div>
      <div className="catalog-banner"><div className="catalog-banner-icon"><PackageX size={22} /></div><div><strong>{copy.integrity.title}</strong><span>{copy.integrity.description}</span></div><span className="catalog-version">{copy.schemaVersion} / <span translate="no">v1alpha1</span></span></div>
      <div className="toolbar-row catalog-toolbar">
        <div className="segmented-control" role="tablist" aria-label={copy.filters.aria}>
          <button className={filter === "all" ? "active" : ""} onClick={() => setFilter("all")} role="tab" aria-selected={filter === "all"}>{copy.filters.all} <span>{games.length}</span></button>
          <button className={filter === "runnable" ? "active" : ""} onClick={() => setFilter("runnable")} role="tab" aria-selected={filter === "runnable"}>{copy.filters.runnable} <span>{games.filter(isUsableGame).length}</span></button>
          <button className={filter === "unavailable" ? "active" : ""} onClick={() => setFilter("unavailable")} role="tab" aria-selected={filter === "unavailable"}>{copy.filters.unavailable} <span>{games.filter(isUnavailableGame).length}</span></button>
        </div>
        <label className="search-input"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={copy.search.placeholder} aria-label={copy.search.aria} /></label>
      </div>
      <div className="catalog-grid">{visible.map((game) => <GameCard key={game.id} game={game} copy={copy} locale={locale} />)}{!visible.length && <div className="empty-state catalog-empty"><Gamepad2 size={25} /><strong>{copy.empty.title}</strong><span>{copy.empty.description}</span></div>}</div>
    </section>
  );
}

function GameCard({ game, copy, locale }: { game: GameDefinition; copy: GamesCopy; locale: Locale }) {
  return <article className={`game-card game-card-${game.status}`}><header><div className={`game-card-icon game-${game.name.toLowerCase().replace(/[^a-z]/g, "")}`}><Gamepad2 size={23} /></div><div className="game-card-heading"><div><h2 translate="no">{game.name} {game.gameVersion}</h2><span translate="no">{game.id}</span></div><StatusBadge tone={game.status === "approved" ? "success" : game.status === "rejected" ? "danger" : "warning"}>{copy.status[game.status]}</StatusBadge></div></header><p>{localizedGameSummary(game, locale)}</p><div className="game-card-meta"><span><Code2 size={14} />{copy.card.definition(game.version)}</span><span>{game.signed ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}{game.signed ? copy.trust.signed : copy.trust.unsigned}</span><span>{game.verified ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}{game.verified ? copy.trust.verified : copy.trust.unverified}</span><span><CircleDot size={14} />{game.runnable ? copy.trust.runnable : copy.trust.unavailable}</span><span><CircleDot size={14} />{game.supported ? copy.trust.supported : copy.trust.unsupported}</span><span><CircleDot size={14} />{copy.card.servers(game.servers)}</span></div><div className="game-card-capabilities">{game.supportReasons.map((reason) => <span key={reason}><CircleAlert size={12} />{copy.reasons[reason] ?? reason}</span>)}{game.capabilities.map((capability) => <span key={capability}><Check size={12} />{localizedGameCapability(capability, locale)}</span>)}</div><footer><span translate="no">{game.platforms.join(" · ")}</span><span>{copy.trust.trustLevel(game.trustLevel)} · {copy.trust.source(game.source)}</span></footer></article>;
}
