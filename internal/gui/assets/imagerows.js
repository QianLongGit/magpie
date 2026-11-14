// ============================================================================
// imagerows.js: Settings › Models › Images 里 Image generation 之后的行，
// 从核心 app.js 搬来（coreScriptBudget：核心只留一行 import）——
//   renderImageGen: 生图模型 + 视频行 + 生图超时（原核心函数，原样）；
//   renderVisionRoute: 视觉路由行——最新 user 消息带图的请求改写到哪个
//     模型（用户 cc-switch fork 的视觉路由内置化的配置口，网关侧判定
//     在 internal/gateway/visionroute.go）。
// 依赖的全局（el、t、icon、namedIn、modelOpt、openPicker、savePrefs、
// libWay、waitRow、featuresOff、gatewayMode、statusErr）由先加载的核心
// 脚本提供；videogen.js 的相对 import 解析相对本文件的同目录。
// ============================================================================

// renderImageGen: the model magpie's generate_image tool draws with — the
// one magpie picks, one named, or none — and where agents are given the tool.
window.renderImageGen = function (s, keep, box) {
  const models = s.imageGenModels || [];
  const named = namedIn(models);
  const icOf = (id) => models.find((x) => x.id === id)?.icon;
  const v = s.imageGen || "";
  // a pick that stands for a listed model shows as it, and is kept as saved
  // until another is picked: a ChatGPT account's gpt-image-2.5 is its one
  // GPT Image, as the backend draws the same for any name (#1865)
  const shown = s.imageGenShown || v;
  // a picked model magpie can't find any more is said, as Image recognition's
  const gone = v && v === s.imageGenMissing;
  const r = el("div", "row pref");
  r.id = "imageGenRow";
  const who = el("div", "who");
  // the Library gives agents the tool, and can be off (#2091, nailuoGG)
  const libOff = featuresOff.has("library");
  const sub = el("div", "sub wraps",
    v === "off" ? t("Agents given Magpie Image can't generate images or videos: the tool says it is off")
    : gatewayMode ? t("The model the Image generation MCP server below draws with, for the agents that add it; images come back in their answer")
    : libOff ? t("The model Magpie Image draws with. Agents get the Magpie Image tool from the Library, which is turned off in Plugins › Built-in; images are saved in their project")
    : t("The model Magpie Image draws with. Agents get the Magpie Image tool from the Library; images are saved in their project"));
  who.append(el("div", "name", t("Image generation")), sub);
  if (v !== "off") who.append(...libWay("magpie-image", t("Give agents Magpie Image…"), "image-gen-tool"));
  // ChatGPT's images backend picks the model itself, so its one entry says
  // so where it draws, picked or automatic (#1865)
  const draws = v === "off" || gone ? "" : v ? shown : s.imageGenAuto;
  if (models.find((x) => x.id === draws)?.vendorPicks) who.append(el("div", "sub image-gen-vendor", t("ChatGPT's image backend picks the GPT Image version itself and doesn't say which; to choose one, use an OpenAI API key")));
  // a pick unticked in its provider's editor since still draws, and says so
  if (s.imageGenHidden && !gone && v !== "off") who.append(el("div", "sub image-gen-hidden", t("{model}, picked here, is unticked in its provider's Image models: it still draws, but leaves this list once another is picked. Tick it there to keep it listed.", { model: named(shown) })));
  if (gone) who.append(el("div", "sub err image-gen-missing", s.imageGenAuto
    ? t("{model}, picked here, isn't set up any more: its provider was removed or turned off, or no longer has it. {auto}, the automatic choice, draws in its place. Pick another model here.", { model: v, auto: named(s.imageGenAuto) })
    : t("{model}, picked here, isn't set up any more: its provider was removed or turned off, or no longer has it. No other model draws, so a request that names no model is turned away. Pick another model here.", { model: v })));
  const b = el("button", "rt-cond on");
  if (v === "off") b.append(el("span", "", t("Off")));
  else if (gone) b.append(icon("generic"), el("span", "", v + " · " + t("missing")));
  else if (v) b.append(icon(icOf(shown) || "generic"), el("span", "", named(shown)));
  else {
    if (s.imageGenAuto) b.append(icon(icOf(s.imageGenAuto) || "generic"));
    b.append(el("span", "", s.imageGenAuto ? t("Automatic") + " · " + named(s.imageGenAuto) : t("Automatic") + " · " + t("no model that draws")));
  }
  // a routing group (no provider of its own) goes with the others, as in
  // an agent's picker, not in a group of its own with its own rail button
  const opt = (x) => ({ value: x.id, label: x.name || x.id, note: x.vendorPicks ? x.providerName + " · " + t("ChatGPT picks the model") : x.providerName, icon: x.icon, group: x.provider ? x.providerName : ROUTING_GROUPS, ref: x.id });
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "imageGen", label: "model", value: shown, options: [
    { value: "", label: t("Automatic"), note: s.imageGenAuto ? named(s.imageGenAuto) : t("no model that draws"), reset: true },
    { value: "off", label: t("Off"), note: t("no images generated"), reset: true },
    ...models.map(opt)],
  onPick: (id) => { if (id !== shown) savePrefs({ ...keep, imageGen: id }); } }, b, ev);
  const val = el("div", "val");
  val.append(b);
  r.append(who, val);
  box.append(r);
  // Video generation's row (#1399), drawn by videogen.js
  if (v !== "off") {
    const vr = el("div", "row pref");
    box.append(vr);
    if (window.videoGenFill) videoGenFill(vr, s, keep);
    else { vr.hidden = true; import("./videogen.js").then(() => videoGenFill(vr, s, keep), () => {}); }
  }
  // how long a vendor is given to draw an image and send it: 5 minutes
  // unless set, as a slow image endpoint needs more (neighbads on X)
  box.append(waitRow("imageTimeoutRow", t("Image generation timeout"), t("How long a provider is given to draw an image and send it, before the request is given up. A slow image endpoint may need longer"),
    [5, 10, 15, 30, 60], s.imageTimeout > 0 ? s.imageTimeout : 5, "{n} min", (n) => savePrefs({ ...keep, imageTimeout: n })));
}

// renderVisionRoute: the Vision route row — the model a request whose
// latest message carries an image is sent to instead, before the gateway
// routes it (the fork's vision branch, built in; see
// internal/gateway/visionroute.go for what hits and what doesn't: history
// images and a plain image path in the text don't, a tool_result image
// does, and a request bound to a group stays on it). Empty leaves every
// request on the model it names. The picker offers the models marked to
// see images (Image recognition's list); anything else written by hand in
// settings.json (a bare name, group/<id>) still serves, shown as is.
window.renderVisionRoute = function (s, keep, box) {
  const models = s.visionModels || [];
  const named = namedIn(models);
  const v = s.visionRoute || "";
  const r = el("div", "row pref");
  r.id = "visionRouteRow";
  const who = el("div", "who");
  who.append(el("div", "name", t("Vision route")), el("div", "sub",
    t("A request whose latest message carries an image is sent to this model instead, before it is routed; empty leaves each request on the model it names")));
  const b = el("button", "rt-cond on");
  const icOf = (id) => models.find((x) => x.id === id)?.icon;
  if (v) b.append(icon(icOf(v) || "generic"), el("span", "", named(v)));
  else b.append(el("span", "", t("Off")));
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "visionRoute", label: "model", value: v, options: [
    { value: "", label: t("Off"), reset: true },
    ...models.map(modelOpt)],
  onPick: (id) => { if (id !== v) savePrefs({ ...keep, visionRoute: id }); } }, b, ev);
  const val = el("div", "val");
  val.append(b);
  r.append(who, val);
  box.append(r);
}
