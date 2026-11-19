// ============================================================================
// imagerows.js: Settings › Models › Images 里 Image generation 之后的行——
//   renderVisionRoute: 视觉路由行——最新 user 消息带图的请求改写到哪个
//     模型（用户 cc-switch fork 的视觉路由内置化的配置口，网关侧判定
//     在 internal/gateway/visionroute.go）。
// 生图模型行（renderImageGen）不再搬到这里：上游把该函数放回了核心
// app.js（顶层 script 的函数会挂到 window），本文件若再带一份旧的，
// 导入时会覆盖核心那份、把上游新加的行（生图分辨率等）顶掉。核心
// app.js 等的就是本文件这一行（window.renderVisionRoute），所以这一
// 份仍会被加载。
// 依赖的全局（el、t、icon、namedIn、modelOpt、openPicker、savePrefs、
// libWay、waitRow、featuresOff、gatewayMode、statusErr）由先加载的核心
// 脚本提供；videogen.js 的相对 import 解析相对本文件的同目录。
// ============================================================================


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
