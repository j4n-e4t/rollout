// Run against intercepted API responses, without SSH or a running web server.
const { test, before, after } = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync, mkdirSync } = require("node:fs");
const { join } = require("node:path");
const playwright = require("playwright");
const html = readFileSync(join(__dirname, "index.html"), "utf8");
const server = name => ({ name, address: name + ".example.test", host: name + ".example.test", deployPath: "/opt/rollout", pinned: true });
const desired = (name, dependsOn = [], networks = ["overleaf_backend"]) => ({ name, image: name + ":latest", dependsOn, networks, replicas: name === "web" ? 2 : 1 });
const model = {
  configPath: "/infra/rollout.json", showUnconfigured: false,
  servers: [server("overleaf"), server("paperless")],
  services: [
    { name: "overleaf", servers: ["overleaf"], hash: "sha256:aaa", path: "services/overleaf", composeFile: "compose.yaml", compose: "services:\n  web:\n    image: overleaf:6.3.0", wait: true, files: ["compose.yaml", "scripts/backup.sh"], topology: { services: [desired("web", ["mongo", "redis"]), desired("mongo"), desired("redis")], networks: ["overleaf_backend"], volumes: [] } },
    { name: "gateway", servers: ["overleaf"], hash: "sha256:bbb", path: "services/gateway", composeFile: "compose.yaml", compose: "services: {}", files: ["compose.yaml"], topology: { services: [desired("proxy", [], ["public"])], networks: ["public"], volumes: [] } },
    { name: "paperless-ngx", servers: ["paperless"], hash: "sha256:ccc", path: "services/paperless", files: [], topology: { services: [desired("app", [], [])] } }
  ]
};
const record = { version: "v6.3.0", hash: "sha256:aaa", message: "Update the document editor", deployedAt: "2026-10-29T10:00:00Z", deployedBy: "julian", rollout: "dev" };
const container = (service, index, extra = {}) => ({
  service, name: `overleaf-${service}-${index}`, image: service + ":latest", state: "running", health: "healthy", status: "Up 2 hours (healthy)",
  startedAt: "2026-10-29T10:00:00Z", restartCount: 0, networks: [{ name: "overleaf_backend", ip: "172.18.0.2" }], mounts: [], ports: [], ...extra
});
const statuses = [
  { server: "overleaf", service: "overleaf", inConfig: true, state: "unhealthy", record, containers: [
    container("web", 1, { dependsOn: ["mongo", "redis"], mounts: [{ type: "volume", name: "web-data-one", destination: "/app/data", rw: true }], ports: [{ container: "80/tcp", hostPort: "8080", hostIp: "0.0.0.0" }], networks: [{ name: "overleaf_backend" }, { name: "public" }] }),
    container("web", 2, { health: "unhealthy", dependsOn: ["mongo"], restartCount: 3, mounts: [{ type: "bind", source: "/srv/cache-two", destination: "/cache", rw: false }] }),
    container("mongo", 1, { mounts: [{ type: "volume", name: "mongo-store", destination: "/data/db", rw: true }] }),
    container("redis", 1)
  ] },
  { server: "overleaf", service: "gateway", inConfig: true, state: "running", containers: [container("proxy", 1, { name: "gateway-proxy-1", networks: [{ name: "public" }] })] },
  { server: "overleaf", service: "log-agent", inConfig: false, state: "running", containers: [container("agent", 1)] },
  { server: "paperless", service: "paperless-ngx", inConfig: true, state: "unreachable", containers: [], error: "SSH connection refused" }
];
let browser;
before(async () => { browser = await playwright[process.env.ROLLOUT_TEST_BROWSER || "webkit"].launch({ headless: true }); });
after(async () => { await browser?.close(); });
async function open(t, { path = "", data = statuses, config = model, failModel = false, viewport = { width: 1440, height: 1000 }, colorScheme = "light" } = {}) {
  const page = await browser.newPage({ viewport, colorScheme, reducedMotion: "reduce" });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  t.after(async () => { await page.close(); assert.deepEqual(errors, [], "No runtime JavaScript errors"); });
  await page.route("http://rollout.test/**", route => {
    const req = route.request(), url = new URL(req.url());
    assert.equal(req.method(), "GET", "Dashboard makes only read requests");
    if (url.pathname.startsWith("/api/")) {
      assert.equal(req.headers()["x-rollout-token"], "fixture-token");
      if (url.pathname === "/api/model" && failModel) return route.fulfill({ status: 500, body: "Invalid rollout.json" });
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(url.pathname === "/api/model" ? config : url.pathname === "/api/status" ? data : [record]) });
    }
    return route.fulfill({ contentType: "text/html", body: html });
  });
  await page.goto("http://rollout.test/?token=fixture-token" + path);
  await page.waitForFunction(() => !document.getElementById("refresh").disabled);
  return page;
}
const count = async (page, selector, n) => assert.equal(await page.locator(selector).count(), n, selector);

test("breadcrumbs select server then stack; replicas keep their own runtime resources", async t => {
  const page = await open(t);
  assert.equal(await page.locator("h1").textContent(), "Infrastructure");
  await count(page, ".server-card", 2);
  assert.equal(new URL(page.url()).searchParams.has("token"), false);
  assert.equal(await page.locator("#stack-select").isDisabled(), true);
  assert.equal((await page.locator("main").textContent()).includes("log-agent"), false);
  await page.selectOption("#server-select", "overleaf");
  await count(page, ".stack-tile", 2);
  await page.selectOption("#stack-select", "overleaf");
  assert.equal(await page.locator("h1").textContent(), "overleaf");
  await count(page, ".service-group", 3);
  await count(page, ".container-card", 4);
  if (process.env.ROLLOUT_SCREENSHOT_DIR) {
    mkdirSync(process.env.ROLLOUT_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({ path: join(process.env.ROLLOUT_SCREENSHOT_DIR, "desktop.png"), fullPage: true });
  }
  const first = page.locator('[data-container="overleaf-web-1"]');
  const second = page.locator('[data-container="overleaf-web-2"]');
  assert.match(await first.textContent(), /web-data-one/);
  assert.doesNotMatch(await first.textContent(), /cache-two/);
  assert.match(await second.textContent(), /cache-two/);
  assert.doesNotMatch(await second.textContent(), /web-data-one/);
  assert.match(await second.textContent(), /Unhealthy/);
  assert.match(await second.textContent(), /3 restarts/);
  assert.match(await second.textContent(), /read-only/);
  assert.equal(await first.getByRole("link").getAttribute("href"), "http://overleaf.example.test:8080/");
  await first.getByRole("button", { name: "Go to proxy service in gateway", exact: true }).click();
  assert.equal(await page.locator("h1").textContent(), "gateway");
  assert.equal(await page.locator(".service-group.focused").getAttribute("data-service"), "proxy");
  await page.goBack();
  assert.equal(await page.locator("h1").textContent(), "overleaf");
});

test("service links are specific to each replica and focus their destination", async t => {
  const page = await open(t, { path: "#/overleaf/overleaf" });
  assert.equal(await page.locator('[data-container="overleaf-web-2"]').getByRole("button", { name: "Go to proxy service in gateway" }).count(), 0);
  await page.locator('[data-container="overleaf-web-2"]').getByRole("button", { name: "Go to mongo service in overleaf" }).click();
  assert.equal(await page.locator(".service-group.focused").getAttribute("data-service"), "mongo");
  assert.match(await page.locator(".connection-section").textContent(), /not measured traffic/);
});

test("container details, history, source and file views retain deep links", async t => {
  const page = await open(t, { path: "#/overleaf/overleaf" });
  await page.getByRole("button", { name: "Inspect container overleaf-web-2", exact: true }).click();
  await count(page, "#panel .box.focus", 1);
  assert.match(await page.locator("#panel .box.focus").textContent(), /overleaf-web-2/);
  assert.match(await page.locator("#panel .box.focus").textContent(), /cache-two/);
  assert.match(page.url(), /containers\/web\/overleaf-web-2$/);
  await page.keyboard.press("Escape");
  assert.equal(await page.locator("#panel").isVisible(), false);
  await page.getByRole("button", { name: "Deployments", exact: true }).click();
  await page.locator("#panel .deploy").waitFor();
  assert.match(await page.locator("#panel").textContent(), /Update the document editor/);
  await page.locator("#panel").getByRole("button", { name: "Compose", exact: true }).click();
  assert.match(await page.locator("#panel pre").textContent(), /overleaf:6.3.0/);
  await page.locator("#panel").getByRole("button", { name: "Files", exact: true }).click();
  assert.match(await page.locator("#panel pre").textContent(), /scripts\/backup.sh/);
  await page.getByRole("button", { name: "Close details", exact: true }).click();
  await page.reload();
  await page.waitForFunction(() => !document.getElementById("refresh").disabled);
  assert.equal(await page.locator("#server-select").inputValue(), "overleaf");
  assert.equal(await page.locator("#stack-select").inputValue(), "overleaf");
});

test("unreachable hosts show unknown containers rather than placeholder replicas", async t => {
  const page = await open(t, { path: "#/paperless/paperless-ngx" });
  await count(page, ".container-card", 0);
  assert.match(await page.locator("main").textContent(), /Live containers unavailable/);
  assert.match(await page.locator("main").textContent(), /SSH connection refused/);
});

test("config can include unconfigured stacks in the breadcrumb", async t => {
  const page = await open(t, { path: "#/overleaf", config: { ...model, showUnconfigured: true } });
  assert.match(await page.locator("#stack-select").textContent(), /log-agent/);
  await page.selectOption("#stack-select", "log-agent");
  assert.equal(await page.locator("h1").textContent(), "log-agent");
  await count(page, ".container-card", 1);
});

test("missing configuration, invalid routes, and a removed stack are actionable", async t => {
  const failed = await open(t, { failModel: true });
  assert.match(await failed.locator("main").textContent(), /Invalid rollout.json/);
  const removed = await open(t, { path: "#/overleaf/deleted" });
  assert.match(await removed.locator("main").textContent(), /Stack not found/);
  await removed.getByRole("button", { name: "All stacks", exact: true }).click();
  assert.equal(await removed.locator("h1").textContent(), "overleaf");
  const invalid = await open(t, { path: "#/overleaf/overleaf/not-a-tab" });
  assert.match(await invalid.locator("#panel").textContent(), /Live release/);
});

test("mobile and dark layouts fit the viewport", async t => {
  for (const colorScheme of ["light", "dark"]) {
    const page = await open(t, { path: "#/overleaf/overleaf", viewport: { width: 390, height: 844 }, colorScheme });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "No page-wide horizontal overflow");
    await count(page, ".container-card", 4);
    if (process.env.ROLLOUT_SCREENSHOT_DIR) {
      mkdirSync(process.env.ROLLOUT_SCREENSHOT_DIR, { recursive: true });
      await page.screenshot({ path: join(process.env.ROLLOUT_SCREENSHOT_DIR, `mobile-${colorScheme}.png`), fullPage: true });
    }
    await page.getByRole("button", { name: "Inspect container overleaf-web-1", exact: true }).click();
    assert.equal(await page.locator("#panel").isVisible(), true);
    await page.getByRole("button", { name: "Close details", exact: true }).click();
  }
});

test("empty servers and keyboard skip navigation remain usable", async t => {
  const page = await open(t, { path: "#/overleaf", config: { ...model, services: null }, data: [] });
  assert.match(await page.locator("main").textContent(), /Assign a stack to this server/);
  await page.locator(".skip").focus();
  await page.keyboard.press("Enter");
  assert.match(page.url(), /#\/overleaf$/);
  assert.equal(await page.evaluate(() => document.activeElement.id), "main");
  await page.locator("#server-select").focus();
  assert.equal(await page.locator("#server-select").evaluate(el => el.matches(":focus-visible")), true);
});

test("a desired-only service is not presented as a live network peer", async t => {
  const config = structuredClone(model);
  config.services[0].topology.services.push(desired("future", [], ["public"]));
  const page = await open(t, { path: "#/overleaf/overleaf", config });
  assert.match(await page.locator('[data-service="future"]').textContent(), /No deployed containers/);
  assert.equal(await page.locator('[data-container="overleaf-web-1"]').getByRole("button", { name: "Go to future service in overleaf" }).count(), 0);
});
