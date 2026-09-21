import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
// Vite resolves CSS side-effect imports at build time.
// @ts-expect-error CSS modules are provided by the Vite client runtime.
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("应用挂载点不存在");

createRoot(root).render(<StrictMode><App /></StrictMode>);
