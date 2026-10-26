import { expect, test } from "@playwright/test";

// kubexray.com: the product site in front of the player.

test("the site links to the code, the chart and the demo", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("See what changed. See what broke. Share what happened.");
  await expect(page.getByRole("link", { name: "GitHub" }).first()).toHaveAttribute("href", "https://github.com/kubexray/kubexray");
  await expect(page.getByRole("link", { name: "Helm chart", exact: true })).toHaveAttribute("href", /charts\/kubexray/);
  await expect(page.locator(".hero-cmd code")).toHaveText("helm install kubexray oci://ghcr.io/kubexray/charts/kubexray");
  // Every screenshot shown loads (the other theme's are hidden).
  for (const img of await page.locator("img:visible").all()) {
    await img.scrollIntoViewIfNeeded();
    await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)).toBe(true);
  }
  await page.screenshot({ path: test.info().outputPath("site.png"), fullPage: true });
});

test("the demo button opens the incident in the player", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("link", { name: "Play the incident" }).click();
  await expect(page).toHaveURL(/\/play\/\?replay=demo\.kxr/);
  await expect(page.locator(".details .name")).toHaveText("checkout");
});

test("each moment of the record opens the player there", async ({ page }) => {
  await page.goto("/");
  await page.locator(".tape li.incident a").click();
  await expect(page.locator("section.incident h2")).toHaveText("checkout is degraded");
  await expect(page.getByText(/11m 4\ds of 31m/)).toBeVisible(); // T+11:45, where the link points
});

test("the site follows the theme chosen in the product", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", "dark");
  await expect(page.locator("#still-map .still-img img.on-light")).toBeVisible();
  await page.getByRole("button", { name: "Switch to dark mode" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("#still-map .still-img img.on-dark")).toBeVisible();
  await page.goto("/play/"); // one choice for the site and the player
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

test("the site fits a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test("the first screen fills the window, and the beam points to the player", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(page.locator(".record")).toBeInViewport();
  await expect(page.locator("#still-map .still-img")).not.toBeInViewport();
  await page.getByRole("link", { name: "See it in the player" }).click();
  await expect(page.locator("#still-map .still-img")).toBeInViewport();
});

test("the player screenshots switch between the map and the Changes page", async ({ page }) => {
  await page.goto("/");
  const changes = page.getByRole("tab", { name: "Changes" });
  await expect(page.locator("#still-changes")).toBeHidden();
  await changes.click();
  await expect(changes).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#still-map")).toBeHidden();
  await page.getByRole("link", { name: "Open the Changes page in the player" }).click();
  await expect(page).toHaveURL(/\/play\/\?replay=demo\.kxr&view=changes$/);
  await expect(page.locator(".feed-card").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Hour", exact: true })).toHaveAttribute("aria-pressed", "true"); // the range that fits the recording
});
