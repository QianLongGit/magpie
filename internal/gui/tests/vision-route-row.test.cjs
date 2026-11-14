// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Images' Vision route row (the cc-switch fork's vision
// routing, built in): the row names where a request whose latest message
// carries an image is sent to instead; empty shows Off. The picker offers
// Off and each model marked to see images (Image recognition's list); a
// pick is saved as visionRoute and shown, another row's save keeps it
// (prefsKeep), and a value written by hand in settings.json (not in the
// list) shows as is. All five languages, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn, pickItem } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Vision route", sub: "A request whose latest message carries an image is sent to this model instead, before it is routed; empty leaves each request on the model it names", off: "Off" },
  zh: { name: "视觉路由", sub: "最新消息带图的请求在路由前改发到该模型；留空则各请求仍用其指定的模型", off: "关闭" },
  "zh-TW": { name: "視覺路由", sub: "最新訊息帶圖的請求在路由前改發到該模型；留空則各請求仍用其指定的模型", off: "關閉" },
  ja: { name: "ビジョンルーティング", sub: "最新のメッセージに画像を含むリクエストは、ルーティング前に代わりにこのモデルへ送られます。空にすると各リクエストは指定されたモデルのままです", off: "オフ" },
  de: { name: "Bild-Routing", sub: "Eine Anfrage, deren letzte Nachricht ein Bild enthält, geht vor dem Routing stattdessen an dieses Modell; leer lässt jede Anfrage bei dem Modell, das sie nennt", off: "Aus" },
};
const visionModels = [
  { id: "zhipu/glm-5.3-flash", name: "GLM 5.3 Flash", provider: "zhipu", providerName: "Zhipu", icon: "zhipu" },
  { id: "openai/gpt-6-luna", name: "GPT-6 Luna", provider: "openai", providerName: "OpenAI", icon: "openai" },
];

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: "light", redact: true, imageGen: "off", visionModels, visionRoute: st.visionRoute || "" });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        posted.push(JSON.parse(r.request().postData()));
        st.visionRoute = posted.at(-1).visionRoute || "";
      }
      return json(settings());
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

async function open(engine, lang, width, posted, st, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posted, st));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=models");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  const row = page.locator("#imagesList #visionRouteRow");
  await row.waitFor();
  return { page, row, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: the Vision route row picks where an image request goes`, async (t) => {
      const posted = [];
      const st = {};
      const { page, row, errors } = await open(engine, lang, 1100, posted, st, t);
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert((await row.locator(".sub").textContent()).includes(w.sub.split(";")[0]));
      assert.equal(await row.locator("button").textContent(), w.off, "nothing picked shows Off");

      // a model picked saves visionRoute and shows as "Name · Provider"
      await reach(page, row.locator("button"));
      await pickItem(page, row.locator("button"));
      await page.locator("#pop").waitFor({ state: "visible" });
      const items = await page.locator("#list li:not(.group)").allInnerTexts();
      assert(items[0].includes(w.off), "Off comes first");
      assert(items.some((x) => x.includes("GLM 5.3 Flash")) && items.some((x) => x.includes("GPT-6 Luna")), items.join(" | "));
      await pickItem(page, page.locator("#list li:not(.group)", { hasText: "GLM 5.3 Flash" }));
      await page.waitForFunction(() => document.querySelector("#imagesList #visionRouteRow button")?.textContent.includes("GLM 5.3 Flash"));
      assert.equal(posted.at(-1).visionRoute, "zhipu/glm-5.3-flash");
      assert.equal(await row.locator("button").textContent(), "GLM 5.3 Flash · Zhipu");

      // Off clears it
      await pickItem(page, row.locator("button"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await pickItem(page, page.locator("#list li:not(.group)", { hasText: w.off }).first());
      await page.waitForFunction((s) => document.querySelector("#imagesList #visionRouteRow button")?.textContent === s, w.off);
      assert.equal(posted.at(-1).visionRoute, "");

      // a value written by hand in settings.json (not in the list) shows as is
      st.visionRoute = "group/vision";
      await page.evaluate(() => fetch("/api/settings").then((r) => r.json()).then((s) => { prefs = s; state.settings = s; renderSettings(); }));
      await page.waitForFunction(() => document.querySelector("#imagesList #visionRouteRow button")?.textContent.includes("group/vision"));
      // another row's save keeps it (prefsKeep)
      await page.evaluate(() => savePrefs({ ...prefsKeep(prefs), imageGen: "" }));
      for (let i = 0; i < 200 && !(posted.at(-1)?.imageGen === ""); i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1).visionRoute, "group/vision", JSON.stringify(posted.at(-1)));
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine}: the row fits at a narrow width`, async (t) => {
    const posted = [];
    const st = { visionRoute: "zhipu/glm-5.3-flash" };
    const { page, row, errors } = await open(engine, "en", 420, posted, st, t);
    for (const part of [".name", "button"]) {
      const fit = await row.locator(part).first().evaluate((e) => {
        const r = e.getBoundingClientRect();
        return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
      });
      assert(fit, `${part} fits at 420`);
    }
    assert.deepEqual(errors, []);
  });
}
