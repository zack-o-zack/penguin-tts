import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "./fixtures";

for (const theme of ["dark", "light"]) {
  test(`component controls meet automated accessibility checks in ${theme} mode`, async ({
    page,
  }, testInfo) => {
    await page.goto(`/e2e/components.html?theme=${theme}`);
    await expect(page.locator("body")).toHaveCSS(
      "color",
      theme === "dark" ? "rgb(237, 241, 245)" : "rgb(20, 38, 27)",
    );
    await expect(page.locator("[data-slot=calendar]")).toHaveCSS(
      "color",
      theme === "dark" ? "rgb(237, 241, 245)" : "rgb(20, 38, 27)",
    );
    const results = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
      .analyze();
    await testInfo.attach(`component-accessibility-${theme}`, {
      body: JSON.stringify(results, null, 2),
      contentType: "application/json",
    });
    expect(
      results.violations.map(({ id, nodes }) => ({
        id,
        nodes: nodes.map(({ target, failureSummary }) => ({
          target,
          failureSummary,
        })),
      })),
    ).toEqual([]);
  });
}

test("keyboard users can operate controls and return from a dialog", async ({
  page,
}) => {
  await page.goto("/e2e/components.html");
  const checkbox = page.getByRole("checkbox", { name: "Work offline" });
  await checkbox.focus();
  await page.keyboard.press("Space");
  await expect(checkbox).toBeChecked();
  const notifications = page.getByRole("switch", { name: "Notifications" });
  await notifications.focus();
  await page.keyboard.press("Space");
  await expect(notifications).toBeChecked();
  const provider = page.getByRole("combobox", { name: "Provider" });
  await provider.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("option", { name: "Local", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("End");
  await expect(
    page.getByRole("option", { name: "Cloud", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(provider).toHaveText("Cloud");
  const accordion = page.getByRole("button", {
    name: "Where is my data stored?",
  });
  await accordion.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByText("On this device.", { exact: true }),
  ).toBeVisible();
  const slider = page.getByRole("slider", { name: "Speed" });
  await slider.focus();
  await page.keyboard.press("ArrowRight");
  await expect(slider).toHaveAttribute("aria-valuenow", "51");
  const localTab = page.getByRole("tab", { name: "Local voices" });
  await localTab.focus();
  await page.keyboard.press("ArrowRight");
  await expect(page.getByRole("tab", { name: "Cloud voices" })).toBeFocused();
  await expect(page.getByRole("tabpanel")).toHaveText("Cloud voice details");
  const trigger = page.getByRole("button", { name: "Open settings" });
  await trigger.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Settings" });
  await expect(dialog).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Display name" }),
  ).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(
    dialog.getByRole("button", { name: "Close", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("textbox", { name: "Display name" }),
  ).toBeFocused();
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(
    results.violations.map(({ id, nodes }) => ({
      id,
      nodes: nodes.map(({ target, failureSummary }) => ({
        target,
        failureSummary,
      })),
    })),
  ).toEqual([]);
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(trigger).toBeFocused();
});

test("focus stays visible and reduced motion overrides component animations", async ({
  page,
}) => {
  await page.goto("/e2e/components.html");
  const button = page.getByRole("button", { name: "Primary action" });
  await button.focus();
  await expect(button).toBeFocused();
  await expect(button).toHaveCSS("outline-style", "solid");
  await expect(button).toHaveCSS("outline-width", "2px");
  await expect(button).toHaveCSS("outline-color", "rgb(180, 228, 197)");
  await expect(page.getByRole("status", { name: "Loading" })).toHaveCSS(
    "animation-iteration-count",
    "1",
  );
});
