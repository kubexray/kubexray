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
  await page.goto("/");
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

test("the timeline goes back to before the incident", async ({ page }) => {
  await openDemo(page);
  await seek(page, "min");
  await expect(page.getByText("No incidents at this moment")).toBeVisible();
  await seek(page, "max");
  await expect(page.locator("section.incident h2")).toHaveText("checkout is degraded");
});

test("compare shows what changed between two moments", async ({ page }) => {
  await openDemo(page);
  await seek(page, "min");
  await page.getByRole("button", { name: "Compare" }).click();
  await seek(page, "max");
  const panel = page.locator(".compare-panel");
  await expect(panel).toContainText("ConfigMap checkout-config");
  await expect(panel).toContainText("edited by kubectl patch (contents not read)");
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
  await page.goto("/?replay=%2Fdemo.kxr&app=Deployment%2Fshop%2Fcheckout");
  await expect(page.locator("section.incident h2")).toHaveText("checkout is degraded");
  await expect(page.locator(".details .name")).toHaveText("checkout");
});

test("replay links must be https", async ({ page }) => {
  await page.goto("/?replay=http%3A%2F%2Fexample.com%2Fx.kxr");
  await expect(page.getByText("Replay links must start with https://.")).toBeVisible();
});

test("dark mode", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await openDemo(page);
  await shot(page, "incident-dark");
});
