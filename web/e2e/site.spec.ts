import { expect, test } from "@playwright/test";

// kubexray.com: the product site in front of the player.

test("the site links to the code, the chart and the demo", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("The flight recorder for your Kubernetes cluster.");
  await expect(page.getByRole("link", { name: "GitHub" }).first()).toHaveAttribute("href", "https://github.com/kubexray/kubexray");
  await expect(page.getByRole("link", { name: "Helm chart", exact: true })).toHaveAttribute("href", /charts\/kubexray/);
  await expect(page.locator(".cmd code").first()).toHaveText("helm install kubexray oci://ghcr.io/kubexray/charts/kubexray -n kubexray --create-namespace");
  // Every screenshot shown loads (the other theme's are hidden).
  for (const img of await page.locator("img:visible").all()) {
    await img.scrollIntoViewIfNeeded();
    await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)).toBe(true);
  }
  await page.screenshot({ path: test.info().outputPath("site.png"), fullPage: true });
});

test("the demo button opens the incident in the player", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("link", { name: "Watch a recorded incident" }).click();
  await expect(page).toHaveURL(/\/play\/\?replay=demo\.kxr/);
  await expect(page.locator("section.incident h2")).toHaveText("checkout is degraded");
});

test("the site follows the theme chosen in the product", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", "dark");
  await expect(page.locator(".frame img.on-light")).toBeVisible();
  await page.getByRole("button", { name: "Switch to dark mode" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator(".frame img.on-dark")).toBeVisible();
  await page.goto("/play/"); // one choice for the site and the player
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

test("the site fits a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});
