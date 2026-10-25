import "./fonts";
import "./styles.css";
import { applyTheme, savedTheme } from "./lib/theme";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

applyTheme(savedTheme(), false); // before the first paint

const root = document.getElementById("root");
if (!root) throw new Error("index.html has no #root element");
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
