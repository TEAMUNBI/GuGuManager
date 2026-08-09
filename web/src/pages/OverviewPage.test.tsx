import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider, STORAGE_KEY, type Locale } from "../i18n/I18n";
import type { Overview } from "../lib/types";
import { OverviewPage } from "./OverviewPage";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  servers: vi.fn(),
  nodes: vi.fn(),
  audit: vi.fn(),
}));

vi.mock("../lib/api", () => ({ api: mocks }));

const emptyOverview: Overview = {
  environment: "development",
  serverCount: 0,
  runningServerCount: 0,
  onlineNodeCount: 0,
  totalNodeCount: 0,
  queuedOperationCount: 0,
  cpuPercent: 0,
  memoryUsedBytes: 0,
  memoryTotalBytes: 0,
  recentActivity: [],
};

const emptyStateCopy: Array<{ locale: Locale; labels: string[] }> = [
  { locale: "zh-CN", labels: ["还没有服务器", "新建服务器后，运行状态与资源负载会显示在这里。", "还没有可用节点", "连接节点 Agent 后，这里会显示节点容量与健康状态。", "还没有操作记录", "服务器和管理服务的操作会按时间显示在这里。"] },
  { locale: "en", labels: ["No servers yet", "Create a server to see its runtime status and resource load here.", "No nodes connected", "Connect a node agent to see capacity and health here.", "No activity yet", "Server and control plane activity will appear here in chronological order."] },
  { locale: "ja", labels: ["サーバーはまだありません", "サーバーを作成すると、稼働状態とリソース使用量がここに表示されます。", "接続済みのノードはありません", "ノードエージェントを接続すると、容量と健全性がここに表示されます。", "操作履歴はまだありません", "サーバーと管理サービスの操作が時系列でここに表示されます。"] },
  { locale: "ko", labels: ["아직 서버가 없습니다", "서버를 만들면 실행 상태와 리소스 사용량이 여기에 표시됩니다.", "연결된 노드가 없습니다", "노드 에이전트를 연결하면 용량과 상태가 여기에 표시됩니다.", "아직 작업 기록이 없습니다", "서버와 관리 서비스의 작업 내역이 시간순으로 여기에 표시됩니다."] },
];

describe("OverviewPage empty states", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.overview.mockResolvedValue(emptyOverview);
    mocks.servers.mockResolvedValue([]);
    mocks.nodes.mockResolvedValue([]);
    mocks.audit.mockResolvedValue([]);
  });

  afterEach(() => {
    cleanup();
    window.localStorage.clear();
  });

  it.each(emptyStateCopy)("renders server, node, and activity guidance in $locale", async ({ locale, labels }) => {
    window.localStorage.setItem(STORAGE_KEY, locale);

    render(<I18nProvider><MemoryRouter><OverviewPage /></MemoryRouter></I18nProvider>);

    expect(await screen.findByText(labels[0])).toBeInTheDocument();
    labels.slice(1).forEach((label) => expect(screen.getByText(label)).toBeInTheDocument());
    expect(mocks.overview).toHaveBeenCalledOnce();
    expect(mocks.servers).toHaveBeenCalledOnce();
    expect(mocks.nodes).toHaveBeenCalledOnce();
    expect(mocks.audit).toHaveBeenCalledOnce();
  });
});
