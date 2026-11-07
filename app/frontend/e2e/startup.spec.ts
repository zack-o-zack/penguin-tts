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
    name: "What would you like to say?",
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
  await page.getByRole("button", { name: "Collapse sidebar" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Text to speak" }),
  ).toBeInViewport();
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

test("sidebar toggles by keyboard without losing the speech draft", async ({
  page,
}) => {
  await page.setViewportSize({ width: 720, height: 540 });
  await page.goto("/");
  const input = page.getByRole("textbox", { name: "Text to speak" });
  const expandedInputWidth = await input.evaluate(
    (element) => element.clientWidth,
  );
  await input.fill("A little space for my words.");
  await input.press("Enter");
  await input.pressSequentially("A second line.");

  const collapse = page.getByRole("button", { name: "Collapse sidebar" });
  await collapse.focus();
  await page.keyboard.press("Enter");
  const expand = page.getByRole("button", { name: "Expand sidebar" });
  await expect(expand).toBeFocused();
  await expect(expand).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("link", { name: "Text to speech" })).toBeHidden();
  await expect
    .poll(() => input.evaluate((element) => element.clientWidth))
    .toBeGreaterThan(expandedInputWidth);

  await page.keyboard.press("Space");
  await expect(collapse).toHaveAttribute("aria-expanded", "true");
  await expect(input).toHaveValue(
    "A little space for my words.\nA second line.",
  );
  await expect
    .poll(() => input.evaluate((element) => element.clientWidth))
    .toEqual(expandedInputWidth);
  await expect(
    page.getByRole("button", { name: "Generate speech (coming soon)" }),
  ).toBeDisabled();
});

for (const theme of ["dark", "light"]) {
  test(`appearance preferences persist and the focused editor stays accessible in ${theme} mode`, async ({
    page,
  }) => {
    await page.goto("/");
    const appearance = page.getByRole("button", {
      name: "Appearance",
      exact: true,
    });
    await appearance.click();
    const themeSelect = page.getByRole("combobox", {
      name: "Theme",
      exact: true,
    });
    await themeSelect.click();
    await page
      .getByRole("option", {
        name: theme === "dark" ? "Dark" : "Light",
        exact: true,
      })
      .click();
    await expect(themeSelect).toBeFocused();
    const transparency = page.getByRole("slider", {
      name: "Transparency",
      exact: true,
    });
    await transparency.focus();
    await page.keyboard.press("Home");
    await expect(transparency).toHaveAttribute("aria-valuenow", "0");
    const surfaceColor = () =>
      page.getByRole("main").evaluate((element) => {
        if (!element.parentElement)
          throw new Error("The workspace is missing.");
        return getComputedStyle(element.parentElement).backgroundColor;
      });
    const solidColor = await surfaceColor();
    await page.keyboard.press("End");
    await expect(transparency).toHaveAttribute("aria-valuenow", "20");
    await expect.poll(surfaceColor).not.toEqual(solidColor);
    await page.keyboard.press("Escape");
    await expect(appearance).toBeFocused();

    const input = page.getByRole("textbox", { name: "Text to speak" });
    const borderColor = () =>
      input.evaluate((element) => {
        if (!element.parentElement) throw new Error("The composer is missing.");
        return getComputedStyle(element.parentElement).borderColor;
      });
    const unfocusedBorder = await borderColor();
    await input.click();
    await expect(input).toBeFocused();
    await expect(input).toHaveCSS("outline-style", "none");
    await expect.poll(borderColor).not.toEqual(unfocusedBorder);
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);

    await page.reload();
    await appearance.click();
    await expect(
      page.getByRole("combobox", { name: "Theme", exact: true }),
    ).toHaveText(theme === "dark" ? "Dark" : "Light");
    await expect(transparency).toHaveAttribute("aria-valuenow", "20");
    await expect(
      page.getByRole("img", { name: "Penguin", exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("banner")
        .getByRole("button", { name: "Collapse sidebar" }),
    ).toBeVisible();
  });
}

test("header starts native window dragging while controls stay clickable", async ({
  page,
}) => {
  const nativeMessages: string[] = [];
  await page.exposeFunction("recordNativeMessage", (message: string) => {
    nativeMessages.push(message);
  });
  await page.addInitScript(() => {
    const desktopWindow = window as typeof window & {
      recordNativeMessage: (message: string) => Promise<void>;
    };
    Object.defineProperty(window, "chrome", {
      configurable: true,
      value: {
        webview: {
          postMessage: (message: string) => {
            void desktopWindow.recordNativeMessage(message);
          },
        },
      },
    });
  });
  await page.goto("/");

  const header = await page.getByRole("banner").boundingBox();
  const logo = await page.getByRole("img", { name: "Penguin" }).boundingBox();
  if (!header || !logo) throw new Error("The header and logo must be visible.");

  for (const region of [header, logo]) {
    const previousDrags = nativeMessages.filter(
      (message) => message === "wails:drag",
    ).length;
    const x = region.x + region.width / 2;
    const y = region.y + region.height / 2;
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x + 12, y + 4);
    await page.mouse.up();
    await expect
      .poll(
        () =>
          nativeMessages.filter((message) => message === "wails:drag").length,
      )
      .toBe(previousDrags + 1);
  }

  const previousDrags = nativeMessages.filter(
    (message) => message === "wails:drag",
  ).length;
  await page.getByRole("button", { name: "Collapse sidebar" }).click();
  await expect(
    page.getByRole("button", { name: "Expand sidebar" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Appearance", exact: true }).click();
  await expect(
    page.getByRole("slider", { name: "Transparency" }),
  ).toBeVisible();
  expect(
    nativeMessages.filter((message) => message === "wails:drag"),
  ).toHaveLength(previousDrags);
});
