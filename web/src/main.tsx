import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/plus-jakarta-sans/wght.css";
import "@fontsource-variable/noto-sans-sc/wght.css";
import "@fontsource-variable/jetbrains-mono/wght.css";
import { App } from "./app/App";
import "./styles.css";
import "./theme.css";
import "./workbench.css";
import "./liquid-command.css";

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
