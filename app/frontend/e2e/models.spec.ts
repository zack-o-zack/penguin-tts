import { mkdir, readFile, rename } from "node:fs/promises";
import { join } from "node:path";
import AxeBuilder from "@axe-core/playwright";
import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { e2eRoot } from "./paths";

const longWait = { timeout: 20_000 };

async function openModels(page: Page) {
  await page.goto("/#models");
  await expect(
    page.getByRole("heading", { level: 1, name: "Models" }),
  ).toBeVisible();
}

function region(page: Page, title: string) {
  return page.getByRole("region", { name: title, exact: true });
}

function card(page: Page, section: string, name: string) {
  return region(page, section).getByRole("group", { name, exact: true });
}

async function choose(page: Page, row: Locator, item: string) {
  await row.getByRole("button", { name: /^More options/ }).press("Enter");
  await page.getByRole("menuitem", { name: item, exact: true }).press("Enter");
}

function names(browserName: string, journey: string) {
  return {
    display: `Synthetic ${browserName} ${journey}`,
    bundle: `e2e-${browserName}-${journey}`,
  };
}

test("pauses, resumes, and verifies a download", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "pause");
  await openModels(page);
  await card(page, "Available", display)
    .getByRole("button", { name: "Download" })
    .click();

  const job = card(page, "In progress", display);
  await expect(job.getByText("Downloading")).toBeVisible();
  await expect(job.getByRole("progressbar")).toHaveAttribute(
    "aria-valuenow",
    /^[1-9]/,
    longWait,
  );
  await expect(job.getByText(/\/s · .* left/)).toBeVisible(longWait);
  await job.getByRole("button", { name: "Pause", exact: true }).click();
  await expect(job.getByText("Paused")).toBeVisible();
  await expect(
    job.getByRole("button", { name: "Pause", exact: true }),
  ).toBeHidden();

  await job.getByRole("button", { name: "Resume", exact: true }).click();
  const installed = card(page, "Installed", display);
  await expect(installed.getByText("Files verified")).toBeVisible(longWait);
  await choose(page, installed, "Show details");
  await expect(installed.getByText("Managed by Penguin")).toBeVisible();
  await expect(
    installed.getByText("Speech engine compatibility unknown"),
  ).toBeVisible();
  await expect(job).toBeHidden();
});

test("shows a retryable failure and recovers", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "retry");
  await openModels(page);
  await card(page, "Available", display)
    .getByRole("button", { name: "Download" })
    .click();
  const job = card(page, "In progress", display);
  await expect(job.getByText("Failed", { exact: true })).toBeVisible(longWait);
  await expect(
    job.getByText("The download server had a problem."),
  ).toBeVisible();
  await job.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(
    card(page, "Installed", display).getByText("Files verified"),
  ).toBeVisible(longWait);
});

test("cancel removes the job and its progress", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "cancel");
  await openModels(page);
  await card(page, "Available", display)
    .getByRole("button", { name: "Download" })
    .click();
  const job = card(page, "In progress", display);
  await expect(job.getByRole("progressbar")).toHaveAttribute(
    "aria-valuenow",
    /^[1-9]/,
    longWait,
  );
  await choose(page, job, "Cancel");
  const confirm = page.getByRole("alertdialog");
  await expect(confirm.getByText("To keep progress")).toBeVisible();
  await confirm.getByRole("button", { name: "Keep downloading" }).click();
  await expect(confirm).toBeHidden();
  await expect(job.getByText("Downloading")).toBeVisible();

  await choose(page, job, "Cancel");
  await confirm.getByRole("button", { name: "Cancel download" }).click();
  await expect(job).toBeHidden();
  await expect(
    card(page, "Available", display).getByRole("button", { name: "Download" }),
  ).toBeEnabled();
});

test("keeps one job across navigation and reload during a download", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "reload");
  await openModels(page);
  await card(page, "Available", display)
    .getByRole("button", { name: "Download" })
    .click();
  const job = card(page, "In progress", display);
  await expect(job.getByRole("progressbar")).toBeVisible();

  await page.getByRole("link", { name: "Text to speech" }).click();
  await expect(job).toBeHidden();
  await expect(
    page.getByRole("textbox", { name: "Text to speak" }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Models" }).click();
  await expect(job).toHaveCount(1);

  await page.reload();
  await expect(job).toHaveCount(1);
  await expect(
    card(page, "Installed", display).getByText("Files verified"),
  ).toBeVisible(longWait);
  await expect(job).toHaveCount(0);
  await expect(card(page, "Installed", display)).toHaveCount(1);
});

test("references an existing folder and removal keeps its files", async ({
  page,
  browserName,
}) => {
  const { display, bundle } = names(browserName, "reference");
  await openModels(page);
  await choose(page, card(page, "Available", display), "Use existing folder");
  const installed = card(page, "Installed", display);
  await expect(installed.getByText("Files verified")).toBeVisible(longWait);
  await choose(page, installed, "Show details");
  await expect(installed.getByText("External folder")).toBeVisible();

  await choose(page, installed, "Remove from Penguin");
  await expect(
    page.getByText("Your folder and its files stay where they are."),
  ).toBeVisible();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Remove from Penguin" })
    .click();
  await expect(installed).toBeHidden();
  const kept = await readFile(join(e2eRoot, "external", bundle, "model.bin"));
  expect(kept.length).toBeGreaterThan(0);
});

test("copies a folder into managed storage by keyboard", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "copy");
  await openModels(page);
  const available = card(page, "Available", display);
  await choose(page, available, "Copy into Penguin");
  const installed = card(page, "Installed", display);
  await expect(installed.getByText("Files verified")).toBeVisible(longWait);
  await choose(page, installed, "Show details");
  await expect(installed.getByText("Managed by Penguin")).toBeVisible();
  await expect(available).toBeHidden();
});

test("locates a moved folder again", async ({ page, browserName }) => {
  const { display, bundle } = names(browserName, "relocate");
  await openModels(page);
  await choose(page, card(page, "Available", display), "Use existing folder");
  const installed = card(page, "Installed", display);
  await expect(installed.getByText("Files verified")).toBeVisible(longWait);

  await mkdir(join(e2eRoot, "moved"), { recursive: true });
  await rename(
    join(e2eRoot, "external", bundle),
    join(e2eRoot, "moved", bundle),
  );

  await choose(page, installed, "Check files");
  await expect(installed.getByText("Unavailable", { exact: true })).toBeVisible(
    longWait,
  );
  await expect(installed.getByText("locate the folder again")).toBeVisible();

  await choose(page, installed, "Locate again");
  await expect(installed.getByText("Files verified")).toBeVisible(longWait);
  await choose(page, installed, "Show details");
  await expect(installed.getByText(join("moved", bundle))).toBeVisible();
});

test("names the file that fails verification", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "mismatch");
  await openModels(page);
  await choose(page, card(page, "Available", display), "Use existing folder");
  const job = card(page, "In progress", display);
  await expect(job.getByText("A file failed its checksum")).toBeVisible(
    longWait,
  );
  await expect(job.getByText("model.bin")).toBeVisible();
  await choose(page, job, "Dismiss");
  await expect(job).toBeHidden();
});

test("sidebar icon opens a popup that downloads a model", async ({
  page,
  browserName,
}) => {
  const { display } = names(browserName, "popup");
  await page.goto("/");
  const open = page.getByRole("button", { name: "Models and downloads" });
  await open.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Models" });
  await expect(dialog).toBeVisible();
  await card(page, "Available", display)
    .getByRole("button", { name: "Download" })
    .press("Enter");
  await expect(
    card(page, "Installed", display).getByText("Files verified"),
  ).toBeVisible(longWait);
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(open).toBeFocused();
});

test("unreviewed models cannot be downloaded", async ({ page }) => {
  await openModels(page);
  const entry = card(page, "Available", "Synthetic restricted");
  await choose(page, entry, "Show details");
  await expect(entry.getByText("model.bin", { exact: true })).toBeVisible();
  await expect(entry.getByRole("button", { name: "Download" })).toBeDisabled();
  await expect(
    entry.getByText("Download unavailable. License review is not complete."),
  ).toBeVisible();
  await entry.getByRole("button", { name: /^More options/ }).press("Enter");
  await expect(
    page.getByRole("menuitem", { name: "Use existing folder", exact: true }),
  ).toBeEnabled();
  await page.keyboard.press("Escape");
});

test("models page meets accessibility checks and reflows", async ({ page }) => {
  await page.setViewportSize({ width: 720, height: 540 });
  await openModels(page);
  await expect(card(page, "Available", "Synthetic restricted")).toBeVisible();
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  expect(results.violations).toEqual([]);
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
