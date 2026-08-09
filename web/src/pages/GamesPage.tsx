import { useEffect, useMemo, useState } from "react";
import { Check, CircleDot, Code2, Gamepad2, PackageCheck, Search, ShieldCheck } from "lucide-react";
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
    approved: string;
    review: string;
  };
  search: { aria: string; placeholder: string };
  empty: { title: string; description: string };
  status: { approved: string; pending: string; rejected: string };
  card: {
    definition: (version: string) => string;
    signedBundle: string;
    servers: (count: number) => string;
  };
}

const gamesCopy: LocalizedCopy<GamesCopy> = {
  "zh-CN": {
    page: { eyebrow: "资源库 / 游戏模板", title: "游戏模板", description: "管理可用于创建服务器的游戏版本、启动参数和运行包。", loading: "正在读取游戏模板", loadError: "无法加载游戏模板" },
    integrity: { title: "运行包完整性校验已启用", description: "审核通过的模板会绑定到已签名的运行包摘要，确保每次部署使用相同内容。" },
    schemaVersion: "结构版本",
    filters: { aria: "按审核状态筛选游戏模板", all: "全部", approved: "可使用", review: "待审核" },
    search: { aria: "搜索游戏模板", placeholder: "搜索游戏或版本" },
    empty: { title: "没有符合条件的游戏模板。", description: "请调整搜索内容或审核状态。" },
    status: { approved: "可使用", pending: "待审核", rejected: "已拒绝" },
    card: { definition: (version) => `模板版本 ${version}`, signedBundle: "运行包已签名", servers: (count) => `${count} 台服务器正在使用` },
  },
  en: {
    page: { eyebrow: "LIBRARY / GAME TEMPLATES", title: "Game templates", description: "Manage game releases, startup settings, and runtime packages used to create servers.", loading: "Loading game templates", loadError: "Unable to load game templates" },
    integrity: { title: "Runtime package verification is active", description: "Approved templates are pinned to a signed package digest so every deployment uses identical content." },
    schemaVersion: "Schema",
    filters: { aria: "Filter game templates by review status", all: "All", approved: "Available", review: "Needs review" },
    search: { aria: "Search game templates", placeholder: "Search game or version" },
    empty: { title: "No game templates match.", description: "Change the search or review status." },
    status: { approved: "Available", pending: "Needs review", rejected: "Rejected" },
    card: { definition: (version) => `Template version ${version}`, signedBundle: "Signed runtime package", servers: (count) => `Used by ${count} ${count === 1 ? "server" : "servers"}` },
  },
  ja: {
    page: { eyebrow: "ライブラリ / ゲームテンプレート", title: "ゲームテンプレート", description: "サーバー作成に使用するゲームバージョン、起動設定、実行パッケージを管理します。", loading: "ゲームテンプレートを読み込み中", loadError: "ゲームテンプレートを読み込めません" },
    integrity: { title: "実行パッケージの整合性検証は有効です", description: "承認済みテンプレートは署名済みパッケージのダイジェストに固定され、常に同じ内容で配置されます。" },
    schemaVersion: "スキーマ",
    filters: { aria: "審査状態でゲームテンプレートを絞り込む", all: "すべて", approved: "利用可能", review: "審査待ち" },
    search: { aria: "ゲームテンプレートを検索", placeholder: "ゲームまたはバージョンを検索" },
    empty: { title: "条件に一致するゲームテンプレートはありません。", description: "検索語または審査状態を変更してください。" },
    status: { approved: "利用可能", pending: "審査待ち", rejected: "却下" },
    card: { definition: (version) => `テンプレートバージョン ${version}`, signedBundle: "署名済みの実行パッケージ", servers: (count) => `${count} 台のサーバーで使用中` },
  },
  ko: {
    page: { eyebrow: "라이브러리 / 게임 템플릿", title: "게임 템플릿", description: "서버 생성에 사용할 게임 버전, 시작 설정과 실행 패키지를 관리합니다.", loading: "게임 템플릿을 불러오는 중", loadError: "게임 템플릿을 불러올 수 없습니다" },
    integrity: { title: "실행 패키지 무결성 검증이 활성화되었습니다", description: "승인된 템플릿은 서명된 패키지 다이제스트에 고정되어 항상 동일한 내용으로 배포됩니다." },
    schemaVersion: "스키마",
    filters: { aria: "검토 상태별 게임 템플릿 필터", all: "전체", approved: "사용 가능", review: "검토 대기" },
    search: { aria: "게임 템플릿 검색", placeholder: "게임 또는 버전 검색" },
    empty: { title: "조건과 일치하는 게임 템플릿이 없습니다.", description: "검색어나 검토 상태를 변경해 보세요." },
    status: { approved: "사용 가능", pending: "검토 대기", rejected: "거부됨" },
    card: { definition: (version) => `템플릿 버전 ${version}`, signedBundle: "서명된 실행 패키지", servers: (count) => `서버 ${count}개에서 사용 중` },
  },
};

export function GamesPage() {
  const copy = useCopy(gamesCopy);
  const { locale } = useI18n();
  const [games, setGames] = useState<GameDefinition[]>([]);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<"all" | "approved" | "pending">("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    api.games()
      .then(setGames)
      .catch((reason) => setError(reason instanceof Error ? reason.message : copy.page.loadError))
      .finally(() => setLoading(false));
  }, []);

  const visible = useMemo(
    () => games.filter((game) => (filter === "all" || game.status === filter) && `${game.name} ${localizedGameSummary(game, locale)}`.toLowerCase().includes(query.toLowerCase())),
    [filter, games, locale, query],
  );

  if (loading) return <section className="page"><LoadingState label={copy.page.loading} /></section>;
  if (error) return <section className="page"><ErrorState message={error} /></section>;

  return (
    <section className="page games-page">
      <div className="page-heading page-heading-wide"><div><h1>{copy.page.title}</h1><p className="lede">{copy.page.description}</p></div></div>
      <div className="catalog-banner"><div className="catalog-banner-icon"><PackageCheck size={22} /></div><div><strong>{copy.integrity.title}</strong><span>{copy.integrity.description}</span></div><span className="catalog-version">{copy.schemaVersion} / <span translate="no">v1alpha1</span></span></div>
      <div className="toolbar-row catalog-toolbar">
        <div className="segmented-control" role="tablist" aria-label={copy.filters.aria}>
          <button className={filter === "all" ? "active" : ""} onClick={() => setFilter("all")} role="tab" aria-selected={filter === "all"}>{copy.filters.all} <span>{games.length}</span></button>
          <button className={filter === "approved" ? "active" : ""} onClick={() => setFilter("approved")} role="tab" aria-selected={filter === "approved"}>{copy.filters.approved} <span>{games.filter((game) => game.status === "approved").length}</span></button>
          <button className={filter === "pending" ? "active" : ""} onClick={() => setFilter("pending")} role="tab" aria-selected={filter === "pending"}>{copy.filters.review} <span>{games.filter((game) => game.status === "pending").length}</span></button>
        </div>
        <label className="search-input"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={copy.search.placeholder} aria-label={copy.search.aria} /></label>
      </div>
      <div className="catalog-grid">{visible.map((game) => <GameCard key={game.id} game={game} copy={copy} locale={locale} />)}{!visible.length && <div className="empty-state catalog-empty"><Gamepad2 size={25} /><strong>{copy.empty.title}</strong><span>{copy.empty.description}</span></div>}</div>
    </section>
  );
}

function GameCard({ game, copy, locale }: { game: GameDefinition; copy: GamesCopy; locale: Locale }) {
  return <article className={`game-card game-card-${game.status}`}><header><div className={`game-card-icon game-${game.name.toLowerCase().replace(/[^a-z]/g, "")}`}><Gamepad2 size={23} /></div><div className="game-card-heading"><div><h2 translate="no">{game.name} {game.gameVersion}</h2><span translate="no">{game.id}</span></div><StatusBadge tone={game.status === "approved" ? "success" : game.status === "rejected" ? "danger" : "warning"}>{copy.status[game.status]}</StatusBadge></div></header><p>{localizedGameSummary(game, locale)}</p><div className="game-card-meta"><span><Code2 size={14} />{copy.card.definition(game.version)}</span><span><ShieldCheck size={14} />{copy.card.signedBundle}</span><span><CircleDot size={14} />{copy.card.servers(game.servers)}</span></div><div className="game-card-capabilities">{game.capabilities.map((capability) => <span key={capability}><Check size={12} />{localizedGameCapability(capability, locale)}</span>)}</div><footer><span translate="no">{game.platforms.join(" · ")}</span></footer></article>;
}
