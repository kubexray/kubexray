import "./fonts";
import "./styles.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Player } from "./Player";

const root = document.getElementById("root");
if (!root) throw new Error("player.html has no #root element");
createRoot(root).render(
  <StrictMode>
    <Player />
  </StrictMode>,
);
