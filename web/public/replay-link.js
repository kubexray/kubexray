// kubexray.com: replay links from before the site moved the player to /play/
// (/?replay=…) still open the replay. A file, not an inline script: the CSP
// allows no inline code.
if (new URLSearchParams(location.search).has("replay")) location.replace("play/" + location.search + location.hash);
