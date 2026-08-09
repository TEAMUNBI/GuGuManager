import { createContext, useContext, useEffect, useMemo, useState } from "react";
import { Languages } from "lucide-react";

export type Locale = "zh-CN" | "en" | "ja" | "ko";

export interface LocaleOption {
  code: Locale;
  label: string;
}

export const localeOptions: LocaleOption[] = [
  { code: "zh-CN", label: "简体中文" },
  { code: "en", label: "English" },
  { code: "ja", label: "日本語" },
  { code: "ko", label: "한국어" },
];

export type LocalizedCopy<T> = Record<Locale, T>;

interface I18nContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
}

export const STORAGE_KEY = "gugu.locale";
export const DEFAULT_LOCALE: Locale = "en";
const STANDALONE_LOCALE: Locale = "en";
const FORMATTER_DEFAULT_LOCALE: Locale = "en";
let activeLocale: Locale = FORMATTER_DEFAULT_LOCALE;
const I18nContext = createContext<I18nContextValue>({
  // Standalone page modules retain the original Chinese UI until a provider is mounted.
  locale: STANDALONE_LOCALE,
  setLocale: () => undefined,
});

function matchLocale(value?: string | null): Locale | null {
  const normalized = value?.trim().toLowerCase();
  if (!normalized) return null;
  if (normalized.startsWith("zh")) return "zh-CN";
  if (normalized.startsWith("ja")) return "ja";
  if (normalized.startsWith("ko")) return "ko";
  if (normalized.startsWith("en")) return "en";
  return null;
}

function resolveInitialLocale(): Locale {
  try {
    const stored = matchLocale(window.localStorage.getItem(STORAGE_KEY));
    if (stored) return stored;
  } catch {
    // Storage can be unavailable in hardened or private browser contexts.
  }

  for (const language of navigator.languages ?? [navigator.language]) {
    const matched = matchLocale(language);
    if (matched) return matched;
  }
  return "en";
}

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocale] = useState<Locale>(resolveInitialLocale);
  activeLocale = locale;

  useEffect(() => {
    // StrictMode replays mount effects in development. Restore the selected
    // locale during each setup so formatter helpers cannot remain on English
    // after the simulated cleanup.
    activeLocale = locale;
    const previousLang = document.documentElement.getAttribute("lang");
    return () => {
      // Standalone page tests and embedded consumers may render without a provider.
      // Reset the module-level formatter locale when the application unmounts so a
      // prior user's choice cannot leak into the next tree.
      activeLocale = FORMATTER_DEFAULT_LOCALE;
      if (previousLang) document.documentElement.setAttribute("lang", previousLang);
      else document.documentElement.removeAttribute("lang");
    };
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
    try {
      window.localStorage.setItem(STORAGE_KEY, locale);
    } catch {
      // The language still applies for the current session when storage is blocked.
    }
  }, [locale]);

  const value = useMemo(() => ({ locale, setLocale }), [locale]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function getActiveLocale(): Locale {
  return activeLocale;
}

export function useI18n() {
  return useContext(I18nContext);
}

export function useCopy<T>(copy: LocalizedCopy<T>): T {
  return copy[useI18n().locale];
}

const selectorLabels: Record<Locale, string> = {
  "zh-CN": "界面语言",
  en: "Interface language",
  ja: "表示言語",
  ko: "인터페이스 언어",
};

export function LanguageSwitcher({ placement = "default" }: { placement?: "default" | "auth" | "topbar" | "sidebar" }) {
  const { locale, setLocale } = useI18n();
  return (
    <label className={`language-switcher language-switcher--${placement}`}>
      <span className="sr-only">{selectorLabels[locale]}</span>
      <Languages size={16} aria-hidden="true" />
      <select
        aria-label={selectorLabels[locale]}
        value={locale}
        onChange={(event) => setLocale(event.target.value as Locale)}
      >
        {localeOptions.map((option) => <option key={option.code} value={option.code} lang={option.code}>{option.label}</option>)}
      </select>
    </label>
  );
}
