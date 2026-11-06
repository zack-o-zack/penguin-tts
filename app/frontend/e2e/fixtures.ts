import { test as base, expect } from "@playwright/test";

export const test = base.extend({
  page: async ({ page, baseURL }, use, testInfo) => {
    if (!baseURL) {
      throw new Error("The E2E base URL must be configured.");
    }

    const appOrigin = new URL(baseURL).origin;
    const browserErrors: string[] = [];

    page.on("pageerror", (error) => browserErrors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") {
        browserErrors.push(message.text());
      }
    });
    page.on("response", (response) => {
      if (
        new URL(response.url()).origin === appOrigin &&
        response.status() >= 400
      ) {
        browserErrors.push(`${response.status()} ${response.url()}`);
      }
    });
    page.on("requestfailed", (request) => {
      if (new URL(request.url()).origin === appOrigin) {
        browserErrors.push(`Failed request: ${request.url()}`);
      }
    });

    await use(page);

    if (browserErrors.length > 0) {
      await testInfo.attach("browser-errors", {
        body: JSON.stringify(browserErrors, null, 2),
        contentType: "application/json",
      });
    }

    expect(
      browserErrors,
      "The app must not emit runtime or asset errors",
    ).toEqual([]);
  },
});

export { expect };
