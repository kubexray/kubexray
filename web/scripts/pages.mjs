// Finishes kubexray.com: the product site becomes the home page, the player
// moves to /play/ (its links one level up), and the demo replay is copied in.
import { copyFileSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";

const out = "dist-pages";
renameSync(`${out}/site.html`, `${out}/index.html`);
mkdirSync(`${out}/play`, { recursive: true });
const player = readFileSync(`${out}/player.html`, "utf8").replaceAll('="./', '="../');
writeFileSync(`${out}/play/index.html`, player);
rmSync(`${out}/player.html`);
if (existsSync("pages-public/demo.kxr")) {
  copyFileSync("pages-public/demo.kxr", `${out}/play/demo.kxr`);
  copyFileSync("pages-public/demo.kxr", `${out}/demo.kxr`); // links made before the move
}
