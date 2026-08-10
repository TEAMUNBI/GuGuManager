import { expect, test, type Response } from "@playwright/test";

const e2ePort = process.env.GUGU_E2E_PORT ?? "18080";
const e2eOrigin = `http://127.0.0.1:${e2ePort}`;
const e2eEmail = "e2e-admin@gugu.local";
const e2ePassword = "browser-e2e-only-2026";

interface ObservedAPIResponse {
  method: string;
  path: string;
  status: number;
}

function isAPIResponse(response: Response, method: string, path: string): boolean {
  const url = new URL(response.url());
  return url.origin === e2eOrigin
    && response.request().method() === method
    && url.pathname === `/api/v1${path}`;
}

test("uses the real Control Plane for deep links, session login, server detail, and logout", async ({ page, request }) => {
  const observed: ObservedAPIResponse[] = [];
  page.on("response", (response) => {
    const url = new URL(response.url());
    if (url.origin === e2eOrigin && url.pathname.startsWith("/api/v1/")) {
      observed.push({
        method: response.request().method(),
        path: url.pathname,
        status: response.status(),
      });
    }
  });

  const ready = await request.get("/readyz");
  expect(ready.status()).toBe(200);

  await page.goto("/servers");
  await expect(page).toHaveURL(`${e2eOrigin}/login`);
  await expect(page.getByLabel("Email address")).toBeVisible();

  await expect.poll(() => observed.some((response) => response.method === "GET"
    && response.path === "/api/v1/setup/status"
    && response.status === 200)).toBe(true);
  await expect.poll(() => observed.some((response) => response.method === "GET"
    && response.path === "/api/v1/auth/session"
    && response.status === 401)).toBe(true);

  await page.getByLabel("Email address").fill(e2eEmail);
  await page.getByLabel("Password").fill(e2ePassword);
  const loginResponsePromise = page.waitForResponse((response) => isAPIResponse(response, "POST", "/auth/login"));
  await page.getByRole("button", { name: "Sign in" }).click();
  const loginResponse = await loginResponsePromise;
  expect(loginResponse.status()).toBe(200);

  await expect(page.getByRole("heading", { name: "Operations overview" })).toBeVisible();
  await page.getByRole("link", { name: "Servers" }).click();
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();

  const seedServer = page.getByRole("link", { name: /雾港生存服/ }).first();
  await expect(seedServer).toBeVisible();
  await seedServer.click();
  await expect(page).toHaveURL(`${e2eOrigin}/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa`);
  await expect(page.getByRole("heading", { name: "雾港生存服" })).toBeVisible();
  await expect.poll(() => observed.some((response) => response.method === "GET"
    && response.path === "/api/v1/servers"
    && response.status === 200)).toBe(true);
  await expect.poll(() => observed.some((response) => response.method === "GET"
    && response.path === "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    && response.status === 200)).toBe(true);

  const logoutResponsePromise = page.waitForResponse((response) => isAPIResponse(response, "POST", "/auth/logout"));
  await page.getByTitle("Sign out").click();
  const logoutResponse = await logoutResponsePromise;
  expect(logoutResponse.status()).toBe(204);
  await expect(page).toHaveURL(`${e2eOrigin}/login`);

  expect(observed).toEqual(expect.arrayContaining([
    { method: "GET", path: "/api/v1/setup/status", status: 200 },
    { method: "GET", path: "/api/v1/auth/session", status: 401 },
    { method: "POST", path: "/api/v1/auth/login", status: 200 },
    { method: "GET", path: "/api/v1/servers", status: 200 },
    { method: "GET", path: "/api/v1/servers/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", status: 200 },
    { method: "POST", path: "/api/v1/auth/logout", status: 204 },
  ]));
});
