package middleware

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// ============================================================================
// 视觉路由中间件：最新 user 消息携带图片时改写 body.model。
// 语义按用户 cc-switch fork 的原实现移植（proxy/model_mapper.rs 的
// vision 分支 + media_sanitizer.rs 的 latest_user_message_contains_image
// + tool_media.rs 的 tool_output_contains_media，ImagesOnly）：
// 从尾部向前找第一条 role=="user" 的消息，其 content（必须是数组）里
// 任一 block 命中即触发——block.type ∈ {image,image_url,input_image}，
// 或 block.content 递归命中，或 tool_result 的 content 里 tool_output
// 含图片（整体 data URL ≥8KB、stringified JSON 递归、深度上限 32）。
// fixture 用 fork 原测试的真实字节。
// ============================================================================

// fork tool_media.rs 的 large_image_data_url（8800 字节，过 8KB 门槛）。
var largeImageDataURL = "data:image/png;base64," + strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAAAE", 400)

// fork model_mapper.rs body_with_latest_tool_result_image：Read 工具读图的
// 核心形态，图片内嵌在最新 user 消息的 tool_result 里。
const visionToolResultImage = `{"model":"claude-sonnet-4-5","messages":[` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.png"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
	`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}]}`

// fork model_mapper.rs body_with_history_image_only：图片只在历史里。
const visionHistoryImage = `{"model":"claude-sonnet-4-5","messages":[` +
	`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"saw it"}]},` +
	`{"role":"user","content":[{"type":"text","text":"next"}]}]}`

// fork media_sanitizer.rs skips_trailing_assistant：尾部 assistant，从后
// 往前找到的第一条 user 带图（fixture 补了 model 字段，fork 的判定函数
// 测试里没有——改写分支要求 body.model 是字符串）。
const visionTrailingAssistant = `{"model":"claude-sonnet-4-5","messages":[` +
	`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"done"}]}]}`

// fork media_sanitizer.rs detects_latest_image：最新 user 直接 image block。
const visionLatestImageBlock = `{"model":"claude-sonnet-5","messages":[` +
	`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"hello"}]},` +
	`{"role":"user","content":[{"type":"text","text":"look"},` +
	`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}`

// fork text_image_path_then_tool_result_image_two_round_flow 的双轮时序：
// 轮 1 只有路径文本——图片数据尚未到货，不触发。
const visionImagePathRound1 = `{"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"看一下这个图片 ~/xxx.jpg 的内容是什么"}]}]}`

// 轮 2：Read 把图以 base64 image block 放进 tool_result，触发（fixture
// 补了 model 字段，见上）。
const visionImagePathRound2 = `{"model":"claude-sonnet-4-5","messages":[` +
	`{"role":"user","content":[{"type":"text","text":"看一下这个图片 ~/xxx.jpg 的内容是什么"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"~/xxx.jpg"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
	`{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"abc"}}]}]}]}`

// fork rejects_non_anthropic_shapes：Responses input 与 Gemini contents
// 的结构天然不命中（fork 有意把视觉路由限定在 Claude 侧）。
const visionResponsesShape = `{"input":[{"role":"user","content":[{"type":"input_image"}]}]}`
const visionGeminiShape = `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png"}}]}]}`

// fork strips_media_from_json_string_and_nested_content：stringified JSON
// 形态——tool_result 的 content 是一个字符串，解析后内嵌 input_image
// （fixture 补了 model 字段与外层 messages 壳）。
var visionStringifiedJSONImage = `{"model":"m","messages":[{"role":"user","content":[` +
	`{"type":"tool_result","tool_use_id":"t1","content":` +
	jsonString(`{"content":[{"type":"input_text","text":"caption"},{"type":"input_image","image_url":"`+largeImageDataURL+`"}]}`) +
	`}]}]}`

// visionNestedJSONImage 把一个带 payload 的 input_image 包进 n 层数组的
// JSON 文本，整体作为 tool_result 的 content 字符串：字符串形态走不进
// 宽松递归（字符串不是数组），只剩严格判定。fork 的字符串分支解析成
// 功后从 depth+1 起算，所以 part 的 depth 是 n+1——31 层命中，32 层过
// 上限（MAX_MEDIA_TRAVERSAL_DEPTH = 32）。
func visionNestedJSONImage(n int) string {
	inner := `{"type":"input_image","image_url":"https://example.com/a.png"}`
	return `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` +
		jsonString(strings.Repeat(`[`, n)+inner+strings.Repeat(`]`, n)) + `}]}]}`
}

// visionNestedContentChain 把图片 part 包进 n 层 {"content":[…]} 的
// object 链，链首落进 user 消息的 content 数组：数组里的 block 靠
// block.content 递归（fork content_has_image_blocks 的宽松路径），这条
// 链没有深度上限——40 层仍命中，与严格判定的 32 层上限相区别。
func visionNestedContentChain(n int) string {
	inner := `{"type":"input_image"}`
	s := strings.Repeat(`{"content":[`, n) + inner + strings.Repeat(`]}`, n)
	return `{"model":"m","messages":[{"role":"user","content":[` + s + `]}]}`
}

// jsonString 把 s 编码成一个 JSON 字符串字面量（带引号）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// visionUse 把 testdata 的脚本装进一个干净的 plugins.json（单一来源：
// 这里和 internal/gateway 的端到端测试装的都是同一份文件）。
func visionUse(t *testing.T, options map[string]any) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "vision-route.middleware.js"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "vision-route.middleware.js")
	if err := os.WriteFile(p, src, 0o644); err != nil {
		t.Fatal(err)
	}
	e := plugin.Entry{Spec: p}
	if options != nil {
		e.Options = options
	}
	use(t, e)
	return p
}

// visionModelOf 跑一次 onRequest，给出 body 里的 model；ok 说它是否被改写
// （未触发时字节原样返回，undefined 保留原字节）。
func visionModelOf(t *testing.T, body, protocol string) (model string, rewritten bool) {
	t.Helper()
	i := info()
	i.Protocol = protocol
	r := Begin(i)
	got, rej := r.Request([]byte(body))
	r.End()
	if rej != nil {
		t.Fatalf("rejected: %+v", rej)
	}
	if string(got) == body {
		var m struct {
			Model string `json:"model"`
		}
		json.Unmarshal(got, &m)
		return m.Model, false
	}
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("body %s: %v", got, err)
	}
	return m.Model, true
}

func TestVisionRouteRewritesTheLatestImageRequest(t *testing.T) {
	visionUse(t, map[string]any{"model": "vision-model"})
	// want 是 "" 时请求必须一个字节都不改（fork：未命中时行为与装不装
	// 中间件一致）；否则是改写后的 model。
	for _, c := range []struct {
		name     string
		body     string
		protocol string
		want     string
	}{
		// ---- 命中：fork 的三种到图形态 ----
		{"tool_result 内嵌图片，改写", visionToolResultImage, "anthropic", "vision-model"},
		{"尾部 assistant 跳过，往前找到带图 user", visionTrailingAssistant, "anthropic", "vision-model"},
		{"最新 user 的 image block，改写", visionLatestImageBlock, "anthropic", "vision-model"},
		{"双轮时序轮 2：Read 读回图片", visionImagePathRound2, "anthropic", "vision-model"},
		// 外层判定只看 block.type（fork is_image_block_type），无 payload 也命中
		{"user content 里无 payload 的 input_image", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"input_image"}]}]}`, "anthropic", "vision-model"},
		{"user content 里无 payload 的 image_url", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`, "anthropic", "vision-model"},
		// ---- tool_output 判定（tool_result.content 内，严格形态）----
		{"stringified JSON 里内嵌 input_image", visionStringifiedJSONImage, "anthropic", "vision-model"},
		{"tool_result content 整体是 8KB data URL", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + largeImageDataURL + `"}]}]}`, "anthropic", "vision-model"},
		{"stringified 嵌套 31 层（part 的 depth 恰为 32）内命中", visionNestedJSONImage(31), "anthropic", "vision-model"},
		{"stringified 嵌套 32 层过深度上限", visionNestedJSONImage(32), "anthropic", ""},
		// ---- 不命中 ----
		{"历史图片不触发", visionHistoryImage, "anthropic", ""},
		{"双轮时序轮 1：只有图片路径文本", visionImagePathRound1, "anthropic", ""},
		{"content 是字符串，不命中", `{"model":"m","messages":[{"role":"user","content":"看一下 ~/a.png 的内容"}]}`, "anthropic", ""},
		// tool_result.content 是数组时 fork 先走宽松递归（type-only）：
		// 无 payload 的 input_image 也命中
		{"tool_result content 数组里无 payload 的 input_image 命中", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"input_image"}]}]}]}`, "anthropic", "vision-model"},
		{"content 链 40 层仍命中（宽松递归无深度上限）", visionNestedContentChain(40), "anthropic", "vision-model"},
		// 字符串形态只剩严格判定（fork tool_media_kind 要 payload）
		{"stringified 无 payload 的 input_image，不命中", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + jsonString(`{"type":"input_image"}`) + `}]}]}`, "anthropic", ""},
		// data URL 的 8KB 门槛（fork WHOLE_DATA_URL_MIN_BYTES）
		{"小 data URL 不命中", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"data:image/png;base64,YWJj"}]}]}`, "anthropic", ""},
		// fork：body.model 必须是字符串才走 vision 分支
		{"无 model 字段的带图请求不改写", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}`, "anthropic", ""},
		// ---- 结构与协议限定 ----
		{"anthropic 协议下 Responses 形态不命中", visionResponsesShape, "anthropic", ""},
		{"anthropic 协议下 Gemini 形态不命中", visionGeminiShape, "anthropic", ""},
		{"responses 协议直接返回", visionResponsesShape, "responses", ""},
		{"gemini 协议直接返回", visionGeminiShape, "gemini", ""},
		// ---- 意外输入静默不触发 ----
		{"messages 非数组", `{"model":"m","messages":"not an array"}`, "anthropic", ""},
		{"body 非对象", `[{"role":"user"}]`, "anthropic", ""},
		{"没有 user 消息", `{"model":"m","messages":[{"role":"assistant","content":[{"type":"image"}]}]}`, "anthropic", ""},
		{"user 消息 content 为 null", `{"model":"m","messages":[{"role":"user","content":null}]}`, "anthropic", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			model, rewritten := visionModelOf(t, c.body, c.protocol)
			if c.want == "" {
				if rewritten {
					t.Fatalf("改写了不该改写的请求，model=%q", model)
				}
				return
			}
			if !rewritten {
				t.Fatalf("未改写带图请求，model=%q", model)
			}
			if model != c.want {
				t.Fatalf("model=%q, want %q", model, c.want)
			}
		})
	}
}

// 未配置视觉模型（无 options、空串、空白串）时恒不触发——行为与装不装
// 中间件一致。末尾的对照证明上面的"原样"是判定为未配置，而不是脚本
// 什么都没做：同一脚本配上 model 后同一请求被改写（对空脚本这里是红的）。
func TestVisionRouteWithoutAModelNeverTriggers(t *testing.T) {
	for _, o := range []map[string]any{nil, {}, {"model": ""}, {"model": "   "}} {
		visionUse(t, o)
		model, rewritten := visionModelOf(t, visionToolResultImage, "anthropic")
		if rewritten || model != "claude-sonnet-4-5" {
			t.Fatalf("options %v: 带图请求被改写成 %q（改写 %v）", o, model, rewritten)
		}
	}
	visionUse(t, map[string]any{"model": "vision-model"})
	model, rewritten := visionModelOf(t, visionToolResultImage, "anthropic")
	if !rewritten || model != "vision-model" {
		t.Fatalf("对照：同一脚本配上 model 后应改写，得 %q（改写 %v）", model, rewritten)
	}
}

// vision_model 的三种写法（裸名、provider/model、group/<id>）都是把
// 字符串原样写进 body.model，不在中间件里解析——网关自己去认。
// 生产形态是 provider 钉死的 "zhipu/glm-5.3-flash"（图片轮换用便宜
// flash，正文 glm-5.3）。db/fork 侧的大写 "[1M]" 后缀 magpie 不剥
// （窗口标记是小写 "[1m]"，TrimSuffix 大小写敏感）：配了就原样发
// 上游，Zhipu 大概率 400/404——纠正责任在配置文档，中间件不代劳。
func TestVisionRouteTakesTheModelAsWritten(t *testing.T) {
	for _, m := range []string{
		"gpt-image",
		"openai/gpt-image",
		"group/vision",
		"zhipu/glm-5.3-flash",
		"zhipu/glm-5.3-flash[1M]",
	} {
		visionUse(t, map[string]any{"model": m})
		got, rewritten := visionModelOf(t, visionToolResultImage, "anthropic")
		if !rewritten || got != m {
			t.Fatalf("%s: got %q（改写 %v）", m, got, rewritten)
		}
	}
}

// 请求入口的 model 绑到一个组（body.model 以 "group/" 开头）时视觉
// 路由不改写：项目绑定组是「唯一出口」语义，改写到组外模型会击穿
// 它。组内图片轮换由组成员自身能力解决，不在中间件层做组级视觉
// 配置。fork 原实现无此问题——vision 在候选链每个 provider 身上各自
// 生效，不换出口。
func TestVisionRouteKeepsGroupBoundRequests(t *testing.T) {
	visionUse(t, map[string]any{"model": "zhipu/glm-5.3-flash"})
	// 绑组请求带图：model 保持 group/proj-1 不变（无守卫时会被改写）。
	const groupBound = `{"model":"group/proj-1","messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}]}`
	model, rewritten := visionModelOf(t, groupBound, "anthropic")
	if rewritten || model != "group/proj-1" {
		t.Fatalf("绑组请求被改写成 %q（改写 %v）", model, rewritten)
	}
	// 守卫不误伤：非 group 入口（生产正文模型）带图照常改写到 flash。
	const plain = `{"model":"zhipu/glm-5.3","messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}]}`
	model, rewritten = visionModelOf(t, plain, "anthropic")
	if !rewritten || model != "zhipu/glm-5.3-flash" {
		t.Fatalf("非组请求未改写，得 %q（改写 %v）", model, rewritten)
	}
}
