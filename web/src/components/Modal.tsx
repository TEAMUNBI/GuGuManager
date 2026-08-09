import { X } from "lucide-react";
import { useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";
import { type LocalizedCopy, useCopy } from "../i18n/I18n";

interface Props {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: React.ReactNode;
  footer?: React.ReactNode;
  dismissible?: boolean;
}

const focusableSelector = "a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex='-1'])";

const modalCopy: LocalizedCopy<{ eyebrow: string; close: string }> = {
  "zh-CN": { eyebrow: "GuGuManager", close: "关闭对话框" },
  en: { eyebrow: "CONTROL PLANE", close: "Close dialog" },
  ja: { eyebrow: "コントロールプレーン", close: "ダイアログを閉じる" },
  ko: { eyebrow: "컨트롤 플레인", close: "대화 상자 닫기" },
};

export function Modal({ open, title, description, onClose, children, footer, dismissible = true }: Props) {
  const copy = useCopy(modalCopy);
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const reactId = useId().replaceAll(":", "");
  const titleId = `modal-title-${reactId}`;
  const descriptionId = `modal-description-${reactId}`;
  closeRef.current = onClose;

  useEffect(() => {
    if (open) return;
    const rememberTrigger = (event: Event) => {
      const target = event.target;
      if (!(target instanceof Element) || dialogRef.current?.contains(target)) return;
      const appRoot = document.getElementById("root");
      if (appRoot && !appRoot.contains(target)) return;
      const focusTarget = target.closest<HTMLElement>(focusableSelector);
      if (focusTarget) returnFocusRef.current = focusTarget;
    };
    document.addEventListener("pointerdown", rememberTrigger, true);
    document.addEventListener("focusin", rememberTrigger, true);
    return () => {
      document.removeEventListener("pointerdown", rememberTrigger, true);
      document.removeEventListener("focusin", rememberTrigger, true);
    };
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const previous = returnFocusRef.current ?? document.activeElement as HTMLElement | null;
    const appRoot = document.getElementById("root");
    const rootWasInert = appRoot?.hasAttribute("inert") ?? false;
    const previousOverflow = document.body.style.overflow;
    appRoot?.setAttribute("inert", "");
    document.body.style.overflow = "hidden";

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && dismissible) {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab" || !dialogRef.current) return;
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(focusableSelector));
      if (!focusable.length) {
        event.preventDefault();
        dialogRef.current.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable.at(-1) as HTMLElement;
      if (event.shiftKey && (document.activeElement === first || !dialogRef.current.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    const focusTimer = window.setTimeout(() => {
      const dialog = dialogRef.current;
      const target = dialog?.querySelector<HTMLElement>("[autofocus]")
        ?? dialog?.querySelector<HTMLElement>(".modal-body input:not([disabled]), .modal-body textarea:not([disabled]), .modal-body select:not([disabled])")
        ?? dialog?.querySelector<HTMLElement>(focusableSelector)
        ?? dialog;
      target?.focus();
    }, 0);
    return () => {
      window.clearTimeout(focusTimer);
      document.removeEventListener("keydown", onKeyDown);
      document.body.style.overflow = previousOverflow;
      if (!rootWasInert) appRoot?.removeAttribute("inert");
      previous?.focus();
    };
  }, [dismissible, open]);
  if (!open) return null;
  return createPortal(
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (dismissible && event.currentTarget === event.target) closeRef.current(); }}>
      <div ref={dialogRef} className="modal" role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={description ? descriptionId : undefined} tabIndex={-1}>
        <header className="modal-header">
          <div><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div>
          <button className="icon-button" type="button" onClick={() => closeRef.current()} aria-label={copy.close} title={copy.close} disabled={!dismissible}><X size={19} /></button>
        </header>
        <div className="modal-body">{children}</div>
        {footer && <footer className="modal-footer">{footer}</footer>}
      </div>
    </div>,
    document.body,
  );
}
