import type { Locale, LocalizedCopy } from "../i18n/I18n";
import type { GameDefinition } from "./types";

const builtInGameSummaries: Record<string, LocalizedCopy<string>> = {
  "io.gugumanager.papermc": {
    "zh-CN": "高性能 Minecraft Java 专用服务器",
    en: "High-performance Minecraft Java dedicated server",
    ja: "高性能な Minecraft Java 専用サーバー",
    ko: "고성능 Minecraft Java 전용 서버",
  },
  "io.gugumanager.factorio": {
    "zh-CN": "适合多人协作工厂存档的稳定服务器",
    en: "Reliable server for cooperative factory saves",
    ja: "協力プレイ用の工場セーブに適した安定したサーバー",
    ko: "협동 공장 세이브에 적합한 안정적인 서버",
  },
  "io.gugumanager.vintagestory": {
    "zh-CN": "强调探索与持久世界的独立服务器",
    en: "Persistent-world server focused on exploration",
    ja: "探索と永続ワールドを重視した専用サーバー",
    ko: "탐험과 영구 월드에 초점을 둔 전용 서버",
  },
};

const gameCapabilityLabels: LocalizedCopy<Record<string, string>> = {
  "zh-CN": { console: "控制台", query: "状态查询", backup: "备份", update: "更新" },
  en: { console: "Console", query: "Status query", backup: "Backups", update: "Updates" },
  ja: { console: "コンソール", query: "状態照会", backup: "バックアップ", update: "更新" },
  ko: { console: "콘솔", query: "상태 조회", backup: "백업", update: "업데이트" },
};

export function localizedGameSummary(game: Pick<GameDefinition, "id" | "summary">, locale: Locale): string {
  return builtInGameSummaries[game.id]?.[locale] ?? game.summary;
}

export function localizedGameCapability(capability: string, locale: Locale): string {
  return gameCapabilityLabels[locale][capability] ?? capability;
}
