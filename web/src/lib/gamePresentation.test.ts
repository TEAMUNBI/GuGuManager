import { describe, expect, it } from "vitest";
import type { Locale } from "../i18n/I18n";
import { localizedGameCapability, localizedGameSummary } from "./gamePresentation";

const paper = {
  id: "io.gugumanager.papermc",
  summary: "Backend fallback summary",
};

describe("game presentation copy", () => {
  it.each([
    ["zh-CN", "高性能 Minecraft Java 专用服务器", "状态查询"],
    ["en", "High-performance Minecraft Java dedicated server", "Status query"],
    ["ja", "高性能な Minecraft Java 専用サーバー", "状態照会"],
    ["ko", "고성능 Minecraft Java 전용 서버", "상태 조회"],
  ] satisfies Array<[Locale, string, string]>)
  ("localizes built-in summaries and capability labels in %s", (locale, summary, queryLabel) => {
    expect(localizedGameSummary(paper, locale)).toBe(summary);
    expect(localizedGameCapability("query", locale)).toBe(queryLabel);
  });

  it("preserves backend copy and unknown capability identifiers", () => {
    expect(localizedGameSummary({ id: "custom.game", summary: "Custom summary" }, "ja")).toBe("Custom summary");
    expect(localizedGameCapability("custom-capability", "ko")).toBe("custom-capability");
  });
});
