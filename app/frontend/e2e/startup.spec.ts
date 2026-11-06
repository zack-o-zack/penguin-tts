import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "./fixtures";

test("boots and reloads without external services", async ({
  page,
  baseURL,
}) => {
  if (!baseURL) {
    throw new Error("The E2E base URL must be configured.");
  }

  const appOrigin = new URL(baseURL).origin;
  const externalRequests: string[] = [];

  await page.route("**/*", (route) => {
    if (new URL(route.request().url()).origin === appOrigin) {
      return route.continue();
    }

    externalRequests.push(route.request().url());
    return route.abort("blockedbyclient");
  });

  await page.goto("/");
  await expect(page).toHaveTitle("Penguin");
  const heading = page.getByRole("heading", {
    level: 1,
    name: "Switch providers, not your workflow.",
  });
  await expect(heading).toBeVisible();

  await page.reload();
  await expect(heading).toBeVisible();
  expect(
    externalRequests,
    "Startup must not depend on third-party services",
  ).toEqual([]);
});

test("remains accessible at the minimum desktop window size", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 720, height: 540 });
  await page.goto("/");
  await expect(page.getByRole("main")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1 })).toBeInViewport();
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    )
    .toBeLessThanOrEqual(0);

  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();

  await testInfo.attach("accessibility-results", {
    body: JSON.stringify(accessibility, null, 2),
    contentType: "application/json",
  });
  expect(accessibility.violations).toEqual([]);
});

test("content reflows at a 320 CSS pixel viewport", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 });
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    )
    .toBeLessThanOrEqual(0);
});
