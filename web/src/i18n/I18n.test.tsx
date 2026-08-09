import { StrictMode, useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { powerLabel } from "../lib/format";
import { I18nProvider, STORAGE_KEY } from "./I18n";

function FormatterProbe() {
  const [, rerender] = useState(0);
  return (
    <div>
      <span>{powerLabel.running}</span>
      <button type="button" onClick={() => rerender((value) => value + 1)}>rerender</button>
    </div>
  );
}

describe("I18nProvider formatter locale", () => {
  beforeEach(() => {
    window.localStorage.setItem(STORAGE_KEY, "zh-CN");
  });

  it("keeps formatters on the selected locale after StrictMode replays effects", () => {
    render(
      <StrictMode>
        <I18nProvider><FormatterProbe /></I18nProvider>
      </StrictMode>,
    );

    fireEvent.click(screen.getByRole("button", { name: "rerender" }));

    expect(screen.getByText("运行中")).toBeInTheDocument();
    expect(screen.queryByText("Running")).not.toBeInTheDocument();
  });
});
