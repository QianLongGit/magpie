// ============================================================================
// vision-route.middleware.js: 按请求的视觉模型路由。
//
// 用户 cc-switch fork 的视觉路由（proxy/model_mapper.rs 的 vision 分支
// + media_sanitizer.rs 的 latest_user_message_contains_image + tool_media.rs
// 的 tool_output_contains_media）移植为 magpie 网关中间件。
//
// 语义：请求 messages 从尾部向前找第一条 role=="user" 的消息（跳过尾部
// assistant）；该消息 content 必须是数组，数组内任一 block 命中即把
// body.model 无条件改写为配置的视觉模型：
//   - block.type ∈ {image, image_url, input_image}（只看 type）；
//   - block.content 递归命中；
//   - block.type=="tool_result" → 对其 content 按 tool_output 规则判定：
//     整体 data URL（≥8KB）、stringified JSON 递归、图片 part 需带
//     payload（fork tool_media_kind 的严格形态），深度上限 32。
// 历史消息里的图片不触发（只看最新 user 轮）；非 Anthropic 协议与
// Responses input / Gemini contents 形态不命中——fork 有意把视觉路由
// 限定在 Claude 侧。用户只发图片路径文本不触发（判定依据是图片数据
// 到货）。未配置视觉模型时恒不触发，行为与装不装中间件一致。
//
// 请求入口绑到组（body.model 以 "group/" 开头）时不改写：项目绑定组
// 是「唯一出口」语义，改写到组外模型会击穿绑定；组内图片轮换由组
// 成员自身能力解决。fork 原实现无此问题——vision 在候选链每个
// provider 身上各自生效，不换出口。
//
// 配置（magpie plugin options vision-route '{"model":"..."}'）：
//   {"model": "zhipu/glm-5.3-flash"}   推荐写法：provider 钉死的生产
//                                       形态（图片轮换用便宜 flash，
//                                       正文 glm-5.3——生产 catalog 导入
//                                       核实：flash 原生多模态，glm-5.3
//                                       纯文本，与 fork 的设计自洽）
//   {"model": "group/vision"}          裸名 / group/<id> 也原样写入，
//                                       网关自己去认
// ⚠ 别写大写 "[1M]" 后缀：magpie 的窗口标记是小写 "[1m]"（provider
// Resolve 的 TrimSuffix 大小写敏感），大写 [1M] 不被剥离、原样发上游，
// Zhipu 大概率 400/404。小写 [1m] 会被剥掉、可用但没必要。中间件对
// 配置值不做任何纠正——照抄写错的责任在配置文档。
// ============================================================================

const DATA_URL_MIN_BYTES = 8 * 1024 // fork WHOLE_DATA_URL_MIN_BYTES
const MAX_DEPTH = 32                // fork MAX_MEDIA_TRAVERSAL_DEPTH

export function onRequest(body, ctx) {
  if (!body || typeof body !== "object") return
  if (ctx.protocol !== "anthropic") return
  const to = ctx.options && typeof ctx.options.model === "string" ? ctx.options.model.trim() : ""
  if (!to) return
  // fork：body.model 必须是字符串才走视觉分支
  if (typeof body.model !== "string") return
  // 绑组请求不改写（见头部「组绑定」节）：组是唯一出口，改写即击穿
  if (body.model.indexOf("group/") === 0) return
  if (!latestUserHasImage(body)) return
  body.model = to
  return body
}

// ---------------------------------------------------------------------------
// 请求级图片检测：仅当最后一条 user 消息携带图片（含 tool_result 内嵌）
// 时命中。Claude Code 会话中图片一旦读入就常驻历史，全量扫描会把会话
// 后半程所有请求都判为图片请求，所以只认尾部最新一轮。
// ---------------------------------------------------------------------------

function latestUserHasImage(body) {
  const messages = body.messages
  if (!Array.isArray(messages)) return false
  let latest = null
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]
    if (m && typeof m === "object" && m.role === "user") { latest = m; break }
  }
  if (latest === null) return false
  return contentHasImageBlocks(latest.content)
}

// content 是数组时，任一 block 命中：type 是图片类型（只看 type），
// 或其 content 递归命中 / tool_result 的 content 走 tool_output 判定。
function contentHasImageBlocks(content) {
  if (!Array.isArray(content)) return false
  for (const block of content) {
    if (!block || typeof block !== "object") continue
    const t = block.type
    if (t === "image" || t === "image_url" || t === "input_image") return true
    const nested = block.content
    if (nested === undefined || nested === null) continue
    if (contentHasImageBlocks(nested)) return true
    if (t === "tool_result" && toolOutputHasImage(nested, 0)) return true
  }
  return false
}

// ---------------------------------------------------------------------------
// tool_output 图片判定（fork 的 ImagesOnly scope）：字符串是整体的图片
// data URL（≥8KB）或可解析为 JSON 后递归；数组任一递归；对象是带
// payload 的图片 part，或递归其 .content。深度上限 32。
// ---------------------------------------------------------------------------

function toolOutputHasImage(value, depth) {
  if (depth > MAX_DEPTH) return false
  if (typeof value === "string") {
    const trimmed = value.trim()
    if (trimmed.length >= DATA_URL_MIN_BYTES && isImageBase64DataUrl(trimmed)) return true
    if (trimmed === "") return false
    let parsed
    try { parsed = JSON.parse(trimmed) } catch (e) { return false }
    return toolOutputHasImage(parsed, depth + 1)
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      if (toolOutputHasImage(item, depth + 1)) return true
    }
    return false
  }
  if (value && typeof value === "object") {
    if (imageToolPart(value)) return true
    const nested = value.content
    if (nested === undefined || nested === null) return false
    return toolOutputHasImage(nested, depth + 1)
  }
  return false
}

// 图片 part 的严格形态（fork tool_media_kind，ImagesOnly 只认图片）：
// input_image/image_url 需有非空 image_url；image 需有 source/data 载荷；
// 无 type 键、image_url 指向 data: URL 的裸 part 也算（loose data URL）。
// type 键存在但不是字符串（含 null）时哪条都不进——fork 的 Option 语义。
function imageToolPart(part) {
  if (part.type !== undefined) {
    const t = part.type
    if (t === "input_image" || t === "image_url") return normalizedImageUrl(part) !== null
    if (t === "image") return typedImageHasPayload(part)
    return false
  }
  return looseDataImageUrl(part)
}

// image_url 归一化（fork normalized_image_url）：字符串或 {url} 对象里
// 非空白的 url；返回该 url，否则 null。
function normalizedImageUrl(part) {
  const iu = part.image_url
  if (typeof iu === "string") return iu.trim() === "" ? null : iu
  if (iu && typeof iu === "object" && typeof iu.url === "string" && iu.url.trim() !== "") return iu.url
  return null
}

// 无 type 字段、image_url 指向 data: URL 的裸图片 part。
function looseDataImageUrl(part) {
  const url = normalizedImageUrl(part)
  if (url === null) return false
  return url.length >= 5 && url.substring(0, 5).toLowerCase() === "data:"
}

// anthropic 的 {type:"image"} 需有载荷（fork typed_image_has_payload）：
// source 是图片 mime（或未声明）且带 url/data；或顶层 data + 图片 mimeType。
function typedImageHasPayload(part) {
  const source = part.source
  if (source && typeof source === "object" && !Array.isArray(source)) {
    if (sourceMediaTypeIsImage(source)) {
      const url = source.url
      const data = source.data
      if ((typeof url === "string" && url.trim() !== "") || (typeof data === "string" && data !== "")) {
        return true
      }
    }
  }
  const data = part.data
  if (typeof data !== "string" || data === "") return false
  const mime = part.mimeType !== undefined ? part.mimeType : part.mime_type
  return typeof mime === "string" && mime.length >= 6 && mime.substring(0, 6).toLowerCase() === "image/"
}

// source 的 media_type/mime_type/mimeType 取第一个存在的键：未声明或
// 非字符串都视为可能是图片（fork is_none_or），字符串须以 image/ 开头。
function sourceMediaTypeIsImage(source) {
  const keys = ["media_type", "mime_type", "mimeType"]
  for (const k of keys) {
    if (source[k] !== undefined) {
      if (typeof source[k] !== "string") return true
      return source[k].length >= 6 && source[k].substring(0, 6).toLowerCase() === "image/"
    }
  }
  return true
}

// 整体的图片 data URL：header（逗号前，小写）以 data:image/ 开头且以
// ;base64 结尾（fork is_image_base64_data_url）。
function isImageBase64DataUrl(value) {
  const comma = value.indexOf(",")
  if (comma < 0) return false
  const header = value.substring(0, comma).toLowerCase()
  if (header.indexOf("data:image/") !== 0) return false
  return header.length >= 7 && header.substring(header.length - 7) === ";base64"
}
