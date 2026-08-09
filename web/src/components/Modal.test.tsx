import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Modal } from "./Modal";

describe("Modal", () => {
  it("keeps keyboard focus inside the dialog", () => {
    render(
      <Modal
        open
        title="Confirm operation"
        description="Review the target before continuing."
        onClose={vi.fn()}
        footer={<button type="button">Confirm</button>}
      >
        <input aria-label="Target" />
      </Modal>,
    );

    const close = screen.getByRole("button", { name: /close dialog|关闭对话框/i });
    const confirm = screen.getByRole("button", { name: "Confirm" });
    confirm.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(close).toHaveFocus();

    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(confirm).toHaveFocus();
  });

  it("restores focus to the element that opened the dialog", async () => {
    function Harness() {
      const [open, setOpen] = useState(false);
      return <div id="root">
        <button type="button" onClick={() => setOpen(true)}>Open dialog</button>
        <Modal open={open} title="Edit entry" onClose={() => setOpen(false)}>
          <input aria-label="Entry name" />
        </Modal>
      </div>;
    }

    render(<Harness />);
    const trigger = screen.getByRole("button", { name: "Open dialog" });
    trigger.focus();
    fireEvent.pointerDown(trigger);
    trigger.blur();
    fireEvent.click(trigger);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Entry name" })).toHaveFocus());

    fireEvent.keyDown(document, { key: "Escape" });

    expect(trigger).toHaveFocus();
  });

  it("isolates the app and cannot be dismissed while a submission is active", () => {
    const onClose = vi.fn();
    const view = render(
      <div id="root">
        <Modal open title="Submitting" onClose={onClose} dismissible={false}>
          <span>Operation in progress</span>
        </Modal>
      </div>,
    );
    const appRoot = document.getElementById("root") as HTMLElement;
    const backdrop = document.querySelector(".modal-backdrop") as HTMLElement;

    expect(appRoot).toHaveAttribute("inert");
    expect(document.body.style.overflow).toBe("hidden");
    expect(screen.getByRole("button", { name: /close dialog|关闭对话框/i })).toBeDisabled();
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.mouseDown(backdrop);
    expect(onClose).not.toHaveBeenCalled();

    view.rerender(<div id="root"><Modal open={false} title="Submitting" onClose={onClose}><span /></Modal></div>);
    expect(appRoot).not.toHaveAttribute("inert");
    expect(document.body.style.overflow).toBe("");
  });
});
