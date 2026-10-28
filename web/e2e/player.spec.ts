import { expect, type Page, test } from "@playwright/test";

// Screenshots of the main views, kept as CI artifacts.
async function shot(page: Page, name: string) {
  await page.screenshot({ path: test.info().outputPath(`${name}.png`), animations: "disabled" }); // pods fade in
}

// Moves the timeline to its start or end, as dragging the slider does.
async function seek(page: Page, to: "min" | "max") {
  await page.locator(".track input").evaluate((el, which) => {
    const input = el as HTMLInputElement;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, input[which]);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }, to);
}

async function openDemo(page: Page) {
  await page.goto("/play/");
  await page.getByRole("button", { name: "Watch a demo incident" }).click();
  await expect(page.locator("section.incident h2")).toHaveText("checkout is degraded");
}

test("the demo incident explains itself", async ({ page }) => {
  await openDemo(page);
  await expect(page.locator("section.incident")).toContainText("ConfigMap checkout-config was edited by kubectl patch");
  await expect(page.getByText("Replay", { exact: true })).toBeVisible();
  await expect(page.locator(".pod").first()).toBeVisible();
  await shot(page, "incident");
});

test("the timeline goes back to before the incident, and on past its end", async ({ page }) => {
  await openDemo(page); // lands on the incident
  await seek(page, "min");
  await expect(page.getByText("No incidents at this moment")).toBeVisible();
  await seek(page, "max"); // after the edit back: recovered
  await expect(page.getByText("No incidents at this moment")).toBeVisible();
});

test("compare shows what changed between two moments", async ({ page }) => {
  await openDemo(page);
  await seek(page, "min");
  await page.getByRole("button", { name: "Compare" }).click();
  await seek(page, "max");
  const panel = page.locator(".compare-panel");
  await expect(panel).toContainText("ConfigMap checkout-config");
  await expect(panel).toContainText("changed by kubectl patch"); // edited, then put back: no net change
  await expect(panel).toContainText("Deployment checkout");
  await shot(page, "compare");
  await page.getByRole("button", { name: "Stop comparing" }).click();
  await expect(panel).toHaveCount(0);
});

test("a post-mortem draft is one click away", async ({ page }) => {
  await openDemo(page);
  await page.getByRole("button", { name: "Post-mortem draft" }).click();
  const draft = page.getByLabel("Post-mortem draft in Markdown");
  await expect(draft).toHaveValue(/^# Post-mortem \(draft\): checkout is degraded/);
  await expect(draft).toHaveValue(/## Timeline \(UTC\)/);
  await expect(draft).toHaveValue(/ConfigMap checkout-config was edited by kubectl patch/);
  await shot(page, "postmortem");
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page.locator("dialog.postmortem")).toHaveCount(0);
});

test("a link opens the replay with the app selected", async ({ page }) => {
  await page.goto("/play/?replay=demo.kxr&app=Deployment%2Fshop%2Fcheckout");
  await expect(page.locator("section.incident h2")).toHaveText("checkout is degraded");
  await expect(page.locator(".details .name")).toHaveText("checkout");
});

test("links made before the player moved to /play/ still open the replay", async ({ page }) => {
  await page.goto("/?replay=%2Fdemo.kxr&app=Deployment%2Fshop%2Fcheckout");
  await expect(page).toHaveURL(/\/play\/\?replay=%2Fdemo\.kxr&app=Deployment%2Fshop%2Fcheckout$/);
  await expect(page.locator(".details .name")).toHaveText("checkout");
});

test("replay links must be https or on this site", async ({ page }) => {
  await page.goto("/play/?replay=http%3A%2F%2Fexample.com%2Fx.kxr");
  await expect(page.getByText("Replay links must use https, or be on this site.")).toBeVisible();
});

test("light by default, dark when chosen", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" }); // the system setting doesn't decide
  await openDemo(page);
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", "dark");
  await page.getByRole("button", { name: "Switch to dark mode" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await shot(page, "incident-dark");
  await page.reload(); // remembered
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

test("the change feed says what each change did", async ({ page }) => {
  await openDemo(page);
  await page.getByRole("button", { name: "Changes", exact: true }).click();
  const cards = page.locator(".feed-card");
  await expect(cards).toHaveCount(17); // the demo's 37 minutes
  await expect(cards.filter({ hasText: "api scaled 3 → 4 replicas" }).locator(".verdict")).toHaveText("Healthy");
  await expect(cards.filter({ hasText: "Helm upgrade storefront 1.0.0 → 1.1.0" }).locator(".verdict")).toHaveText("Healthy");
  const configEdits = cards.filter({ hasText: "ConfigMap checkout-config edited" });
  // The edit that broke checkout, fixed since: green, and says so.
  const resolved = configEdits.filter({ hasText: "Resolved" });
  await expect(resolved).toContainText(/broke checkout for .* · resolved at /);
  await expect(resolved).toHaveClass(/resolved/);
  await expect(configEdits.filter({ hasText: "Fixed an incident" })).toContainText("checkout healthy");
  const drift = cards.filter({ hasText: "env PROMO_BANNER added" });
  await expect(drift).toContainText("managed by Helm release storefront"); // the hand edit: drift
  await expect(drift.locator(".drift-restore code")).toContainText("helm get manifest storefront -n shop");
  await expect(cards.filter({ hasText: "env PROMO_BANNER removed" })).toBeVisible(); // put back with that command
  // Requests failing while every pod looks healthy: the Service edit.
  await expect(cards.filter({ hasText: "spec.ports[0].targetPort 80 → 8080" })).toContainText(/broke api for .* · resolved at /);
  await shot(page, "changes");

  await page.getByRole("button", { name: /^Broke something/ }).click();
  await expect(cards).toHaveCount(3);
  await page.getByRole("button", { name: /^All changes/ }).click();
  await page.getByPlaceholder("Search apps, people, images…").fill("checkout-config");
  await expect(cards).toHaveCount(2);
  await page.getByPlaceholder("Search apps, people, images…").fill("");

  // The change that broke checkout, opened, then rewound to on the map.
  await resolved.getByRole("button", { name: "Details" }).click();
  await expect(page.locator("dialog.feed-drawer")).toContainText("What changed");
  await shot(page, "change-detail");
  await page.getByRole("button", { name: "Rewind to just before" }).click();
  await expect(page.locator("dialog.feed-drawer")).toHaveCount(0);
  await expect(page.getByText("No incidents at this moment")).toBeVisible();
  await expect(page.locator(".details .name")).toHaveText("checkout");
});

test("the incidents view lists what broke", async ({ page }) => {
  await openDemo(page);
  await page.getByRole("button", { name: /^Incidents/ }).click();
  await expect(page.getByRole("heading", { name: "Changes that broke something" })).toBeVisible();
  await expect(page.locator(".feed-list").last()).toContainText("ConfigMap checkout-config edited");
});

test("the histogram shows and picks changes", async ({ page }) => {
  await openDemo(page);
  await page.getByRole("button", { name: "Changes", exact: true }).click();
  await page.getByRole("button", { name: "Hour", exact: true }).click();
  const full = page.locator(".hist-bar").filter({ has: page.locator(".seg") });
  await expect(full.first()).toBeVisible();

  await full.first().hover();
  await expect(page.locator(".hist-tip")).toContainText("api scaled 3 → 4 replicas");
  await shot(page, "histogram-tooltip");

  await full.first().click();
  await expect(page.locator(".hist-window")).toContainText("Showing");
  const cards = page.locator(".feed-card");
  const picked = await cards.count();
  expect(picked).toBeLessThan(17);
  await page.locator(".hist-window").getByRole("button", { name: "Clear" }).click();
  await expect(cards).toHaveCount(17);

  // Drag across every bar with changes: all of them again, in a window.
  const first = await full.first().boundingBox();
  const last = await full.last().boundingBox();
  if (!first || !last) throw new Error("no bars");
  await page.mouse.move(first.x + 2, first.y + first.height - 4);
  await page.mouse.down();
  await page.mouse.move(last.x + last.width - 2, last.y + last.height - 4, { steps: 8 });
  await page.mouse.up();
  await expect(page.locator(".hist-window")).toBeVisible();
  await expect(cards).toHaveCount(17);
});

test("see diff shows what changed, value by value", async ({ page }) => {
  await page.goto("/play/?replay=demo.kxr&view=changes");
  const edit = page.locator(".feed-card").filter({ hasText: "ConfigMap checkout-config edited" }).filter({ hasText: "Resolved" });
  await edit.getByRole("button", { name: "See diff" }).click();
  await expect(edit.locator(".diff-line.del")).toContainText("info");
  await expect(edit.locator(".diff-line.add")).toContainText("verbose");
  await expect(edit.getByRole("button", { name: "Hide diff" })).toHaveAttribute("aria-expanded", "true");
});
