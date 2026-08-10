export type Language = "yaml" | "json" | "properties" | "text";

export function detectLanguage(fileName: string): Language {
  const lower = fileName.toLowerCase();
  if (lower.endsWith(".yaml") || lower.endsWith(".yml")) return "yaml";
  if (lower.endsWith(".json")) return "json";
  if (lower.endsWith(".properties")) return "properties";
  return "text";
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

interface Rule {
  cls: string;
  re: RegExp;
}

function tokenize(text: string, rules: Rule[]): string {
  if (!text) return "";
  const source = rules.map((r) => `(${r.re.source})`).join("|");
  const master = new RegExp(source, "g");
  let result = "";
  let last = 0;
  let match: RegExpExecArray | null;
  while ((match = master.exec(text)) !== null) {
    if (match.index > last) {
      result += escapeHtml(text.slice(last, match.index));
    }
    for (let i = 0; i < rules.length; i++) {
      if (match[i + 1] !== undefined) {
        result += `<span class="tok-${rules[i].cls}">${escapeHtml(match[i + 1])}</span>`;
        break;
      }
    }
    last = match.index + match[0].length;
    if (match[0].length === 0) master.lastIndex++;
  }
  result += escapeHtml(text.slice(last));
  return result;
}

const yamlRules: Rule[] = [
  { cls: "comment", re: /#[^\n]*/ },
  { cls: "string", re: /"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/ },
  { cls: "anchor", re: /[&*][\w-]+/ },
  { cls: "boolean", re: /\b(?:true|false|yes|no|on|off)\b/ },
  { cls: "number", re: /\b-?\d+(?:\.\d+)?\b/ },
  { cls: "null", re: /\b(?:null|~)\b/ },
  { cls: "key", re: /^[ \t]*[\w.-]+(?=[ \t]*:)/m },
  { cls: "punct", re: /^[ \t]*- /m },
];

function highlightYaml(text: string): string {
  const lines = text.split("\n");
  return lines.map((line) => tokenize(line, yamlRules)).join("\n");
}

const jsonRules: Rule[] = [
  { cls: "key", re: /"(?:[^"\\]|\\.)*"(?=\s*:)/ },
  { cls: "string", re: /"(?:[^"\\]|\\.)*"/ },
  { cls: "number", re: /\b-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/ },
  { cls: "boolean", re: /\b(?:true|false)\b/ },
  { cls: "null", re: /\bnull\b/ },
  { cls: "punct", re: /[{}[\],:]/ },
];

function highlightJson(text: string): string {
  return tokenize(text, jsonRules);
}

function highlightProperties(text: string): string {
  const lines = text.split("\n");
  return lines
    .map((line) => {
      const commentMatch = line.match(/^\s*[#!].*$/);
      if (commentMatch) {
        return `<span class="tok-comment">${escapeHtml(line)}</span>`;
      }
      const sepIndex = line.search(/[=:]/);
      if (sepIndex < 0) return escapeHtml(line);
      const key = line.slice(0, sepIndex);
      const sep = line[sepIndex];
      const value = line.slice(sepIndex + 1);
      const highlightedValue = tokenize(value, [
        { cls: "string", re: /"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/ },
        { cls: "boolean", re: /\b(?:true|false)\b/ },
        { cls: "number", re: /\b-?\d+(?:\.\d+)?\b/ },
      ]);
      return `<span class="tok-key">${escapeHtml(key)}</span><span class="tok-punct">${escapeHtml(sep)}</span>${highlightedValue}`;
    })
    .join("\n");
}

export function highlight(code: string, language: Language): string {
  if (language === "yaml") return highlightYaml(code);
  if (language === "json") return highlightJson(code);
  if (language === "properties") return highlightProperties(code);
  return escapeHtml(code);
}
