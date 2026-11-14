// ============================================================================
// 视觉路由（内置）：带图请求的模型改写。
//
// 用户 cc-switch fork 的视觉路由（proxy/model_mapper.rs 的 vision 分支
// + media_sanitizer.rs 的 latest_user_message_contains_image + tool_media.rs
// 的 tool_output_contains_media）从 JS 中间件内置化为 magpie 核心功能：
// Settings › Models › Images 的 Vision route 一行配 settings.
// VisionRouteModel，新环境装好即在，不拷贝配置文件。判定语义与
// internal/middleware/testdata/vision-route.middleware.js 同源（两处的
// 测试用同一批 fork 原字节 fixture，防漂移）。
//
// 语义：Anthropic messages 形态的请求，messages 从尾部向前找第一条
// role=="user" 的消息（跳过尾部 assistant）；该消息 content 必须是数组，
// 数组内任一 block 命中即把 body.model 无条件改写为配置的视觉模型：
//   - block.type ∈ {image, image_url, input_image}（只看 type）；
//   - block.content 递归命中；
//   - block.type=="tool_result" → 对其 content 按 tool_output 规则判定：
//     整体 data URL（≥8KB）、stringified JSON 递归、图片 part 需带
//     payload（fork tool_media_kind 的严格形态），深度上限 32。
// 历史消息里的图片不触发（只看最新 user 轮）——Claude Code 会话中图片
// 一旦读入就常驻历史，全量扫描会把会话后半程所有请求都判为图片请求。
// 用户只发图片路径文本不触发（判定依据是图片数据到货）。未配置视觉
// 模型时恒不触发。绑组请求（model 以 "group/" 开头）不改写：组是「唯一
// 出口」语义，改写到组外模型会击穿绑定。Responses input / Gemini
// contents 形态不命中——fork 有意把视觉路由限定在 Claude 侧。
// ============================================================================

package gateway

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// fork tool_media.rs 的常量。
const (
	visionDataURLMinBytes = 8 * 1024 // WHOLE_DATA_URL_MIN_BYTES
	visionMaxDepth        = 32       // MAX_MEDIA_TRAVERSAL_DEPTH
)

// visionRoute 是内置视觉路由的入口：to（settings.VisionRouteModel 去掉
// 两端空白）为空时不触发，行为与没有视觉路由一致；from 非 Anthropic
// 协议直接返回（fork 把视觉路由限定在 Claude 侧）。
func visionRoute(from provider.Protocol, body []byte, to string) []byte {
	to = strings.TrimSpace(to)
	if to == "" || from != provider.Anthropic {
		return body
	}
	return visionRouteBody(to, body)
}

// visionRouteBody 把最新 user 消息带图的请求的 model 改写为 to：to 原样
// 写入（裸名、provider/model、group/<id> 都不在改写里解析，网关自己去
// 认）。改写用 rewriteModel 的字节级替换，其余字段一字节不动——发上游
// 的 body 除 model 外与客户端发来的相同。
func visionRouteBody(to string, body []byte) []byte {
	// fork：body.model 必须是字符串才走视觉分支
	m := gjson.GetBytes(body, "model")
	if m.Type != gjson.String {
		return body
	}
	// 绑组请求不改写：组是唯一出口，改写即击穿（组内图片轮换由组
	// 成员自身能力解决）
	if strings.HasPrefix(m.Str, "group/") {
		return body
	}
	if !latestUserHasImage(body) {
		return body
	}
	return rewriteModel(body, to)
}

// ---------------------------------------------------------------------------
// 请求级图片检测：仅当最后一条 user 消息携带图片（含 tool_result 内嵌）
// 时命中。从尾部向前找第一条 role=="user"，其 content（必须是数组）里
// 任一 block 命中：type 是图片类型（只看 type），或其 content 递归命中
// / tool_result 的 content 走 tool_output 判定。
// ---------------------------------------------------------------------------

func latestUserHasImage(body []byte) bool {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		m := arr[i]
		if m.IsObject() && m.Get("role").Str == "user" {
			return contentHasImageBlocks(m.Get("content"))
		}
	}
	return false
}

func contentHasImageBlocks(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, block := range content.Array() {
		if !block.IsObject() {
			continue
		}
		switch block.Get("type").Str {
		case "image", "image_url", "input_image":
			return true
		}
		nested := block.Get("content")
		if !nested.Exists() || nested.Type == gjson.Null {
			continue
		}
		if contentHasImageBlocks(nested) {
			return true
		}
		if block.Get("type").Str == "tool_result" && toolOutputHasImage(nested, 0) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// tool_output 图片判定（fork 的 ImagesOnly scope）：字符串是整体的图片
// data URL（≥8KB）或可解析为 JSON 后递归；数组任一递归；对象是带
// payload 的图片 part，或递归其 .content。深度上限 32。
// ---------------------------------------------------------------------------

func toolOutputHasImage(value gjson.Result, depth int) bool {
	if depth > visionMaxDepth {
		return false
	}
	switch {
	case value.Type == gjson.String:
		trimmed := strings.TrimSpace(value.Str)
		// 长度按字节计：data URL 是 base64（ASCII），与 fork 的 UTF-16
		// code unit 计数在此形态下相等
		if len(trimmed) >= visionDataURLMinBytes && isImageBase64DataURL(trimmed) {
			return true
		}
		if trimmed == "" {
			return false
		}
		var parsed json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return false
		}
		return toolOutputHasImage(gjson.ParseBytes(parsed), depth+1)
	case value.IsArray():
		for _, item := range value.Array() {
			if toolOutputHasImage(item, depth+1) {
				return true
			}
		}
		return false
	case value.IsObject():
		if imageToolPart(value) {
			return true
		}
		nested := value.Get("content")
		if !nested.Exists() || nested.Type == gjson.Null {
			return false
		}
		return toolOutputHasImage(nested, depth+1)
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 图片 part 的严格形态（fork tool_media_kind，ImagesOnly 只认图片）：
// input_image/image_url 需有非空 image_url；image 需有 source/data 载荷；
// 无 type 键、image_url 指向 data: URL 的裸 part 也算（loose data URL）。
// type 键存在但不是字符串（含 null）时哪条都不进——fork 的 Option 语义。
// ---------------------------------------------------------------------------

func imageToolPart(part gjson.Result) bool {
	if t := part.Get("type"); t.Exists() {
		switch t.Str {
		case "input_image", "image_url":
			_, ok := normalizedImageURL(part)
			return ok
		case "image":
			return typedImageHasPayload(part)
		}
		return false
	}
	return looseDataURL(part)
}

// normalizedImageURL（fork normalized_image_url）：image_url 是字符串或
// {url} 对象里的非空白 url；返回该 url 原文（不 trim），否则未命中。
func normalizedImageURL(part gjson.Result) (string, bool) {
	iu := part.Get("image_url")
	if iu.Type == gjson.String {
		if strings.TrimSpace(iu.Str) == "" {
			return "", false
		}
		return iu.Str, true
	}
	if iu.IsObject() {
		if u := iu.Get("url"); u.Type == gjson.String && strings.TrimSpace(u.Str) != "" {
			return u.Str, true
		}
	}
	return "", false
}

// looseDataURL：无 type 字段、image_url 指向 data: URL 的裸图片 part。
func looseDataURL(part gjson.Result) bool {
	url, ok := normalizedImageURL(part)
	if !ok || len(url) < 5 {
		return false
	}
	return strings.EqualFold(url[:5], "data:")
}

// typedImageHasPayload（fork typed_image_has_payload）：anthropic 的
// {type:"image"} 需有载荷——source 是图片 mime（或未声明）且带 url/data；
// 或顶层 data + 图片 mimeType。
func typedImageHasPayload(part gjson.Result) bool {
	if source := part.Get("source"); source.IsObject() {
		if sourceMediaTypeIsImage(source) {
			u, d := source.Get("url"), source.Get("data")
			if (u.Type == gjson.String && strings.TrimSpace(u.Str) != "") ||
				(d.Type == gjson.String && d.Str != "") {
				return true
			}
		}
	}
	d := part.Get("data")
	if d.Type != gjson.String || d.Str == "" {
		return false
	}
	mime := part.Get("mimeType")
	if !mime.Exists() {
		mime = part.Get("mime_type")
	}
	return mime.Type == gjson.String && len(mime.Str) >= 6 && strings.EqualFold(mime.Str[:6], "image/")
}

// sourceMediaTypeIsImage：source 的 media_type/mime_type/mimeType 取第一
// 个存在的键（哪怕值不是字符串）：未声明或非字符串都视为可能是图片
// （fork is_none_or），字符串须以 image/ 开头。
func sourceMediaTypeIsImage(source gjson.Result) bool {
	for _, k := range []string{"media_type", "mime_type", "mimeType"} {
		if v := source.Get(k); v.Exists() {
			if v.Type != gjson.String {
				return true
			}
			return len(v.Str) >= 6 && strings.EqualFold(v.Str[:6], "image/")
		}
	}
	return true
}

// isImageBase64DataURL（fork is_image_base64_data_url）：整体的图片
// data URL——header（逗号前，小写）以 data:image/ 开头且以 ;base64 结尾。
func isImageBase64DataURL(v string) bool {
	comma := strings.IndexByte(v, ',')
	if comma < 0 {
		return false
	}
	header := strings.ToLower(v[:comma])
	// HasSuffix("";base64") 蕴含 fork 的 header.length >= 7 检查
	return strings.HasPrefix(header, "data:image/") && strings.HasSuffix(header, ";base64")
}
