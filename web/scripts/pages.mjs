// Finishes the static player site: the player becomes the home page, and the
// demo replay is copied in.
import { copyFileSync, existsSync, renameSync } from "node:fs";

renameSync("dist-pages/player.html", "dist-pages/index.html");
if (existsSync("pages-public/demo.kxr")) copyFileSync("pages-public/demo.kxr", "dist-pages/demo.kxr");
