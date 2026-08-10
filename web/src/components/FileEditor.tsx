import { useMemo, useRef } from "react";
import { detectLanguage, highlight, type Language } from "../lib/syntax";

interface Props {
  value: string;
  onChange: (value: string) => void;
  fileName: string;
  readOnly?: boolean;
  ariaLabel?: string;
}

export function FileEditor({ value, onChange, fileName, readOnly = false, ariaLabel }: Props) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const preRef = useRef<HTMLPreElement>(null);

  const language: Language = useMemo(() => detectLanguage(fileName), [fileName]);
  const highlighted = useMemo(() => highlight(value + "\n", language), [value, language]);

  const handleScroll = () => {
    if (preRef.current && textareaRef.current) {
      preRef.current.scrollTop = textareaRef.current.scrollTop;
      preRef.current.scrollLeft = textareaRef.current.scrollLeft;
    }
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Tab") {
      event.preventDefault();
      const target = event.currentTarget;
      const start = target.selectionStart;
      const end = target.selectionEnd;
      const next = value.slice(0, start) + "  " + value.slice(end);
      onChange(next);
      requestAnimationFrame(() => {
        target.selectionStart = target.selectionEnd = start + 2;
      });
    }
  };

  return (
    <div className="code-editor">
      <pre ref={preRef} className="code-editor-highlight" aria-hidden="true" dangerouslySetInnerHTML={{ __html: highlighted }} />
      <textarea
        ref={textareaRef}
        className="code-editor-textarea"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onScroll={handleScroll}
        onKeyDown={handleKeyDown}
        spellCheck={false}
        readOnly={readOnly}
        disabled={readOnly}
        aria-label={ariaLabel}
        autoCapitalize="off"
        autoCorrect="off"
        autoComplete="off"
        translate="no"
      />
    </div>
  );
}
