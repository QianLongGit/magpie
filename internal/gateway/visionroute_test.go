package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// ============================================================================
// 视觉路由（内置）：最新 user 消息携带图片时改写 body.model。
// 语义与 internal/middleware/testdata/vision-route.middleware.js 同源
// （用户 cc-switch fork 的 proxy/model_mapper.rs vision 分支 +
// media_sanitizer.rs 的 latest_user_message_contains_image + tool_media.rs
// 的 tool_output_contains_media，ImagesOnly）——fixture 也用同一批字节，
// 防两处漂移。与 JS 中间件的差别只在配置来源：Settings › Models ›
// Images 的 Vision route 一行（settings.VisionRouteModel），装不装插件
// 都在，新环境零配置文件可用。
// ============================================================================

// fork tool_media.rs 的 large_image_data_url（8800 字节，过 8KB 门槛）。
var visionLargeImageDataURL = "data:image/png;base64," + strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAAAE", 400)

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
// 往前找到的第一条 user 带图。
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
// 补了 model 字段，fork 的判定测试里没有——改写分支要求它是字符串）。
const visionImagePathRound2 = `{"model":"claude-sonnet-4-5","messages":[` +
	`{"role":"user","content":[{"type":"text","text":"看一下这个图片 ~/xxx.jpg 的内容是什么"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"~/xxx.jpg"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
	`{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"abc"}}]}]}]}`

// fork rejects_non_anthropic_shapes：Responses input 与 Gemini contents 的
// 结构天然不命中（fork 有意把视觉路由限定在 Claude 侧）。
const visionResponsesShape = `{"input":[{"role":"user","content":[{"type":"input_image"}]}]}`
const visionGeminiShape = `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png"}}]}]}`

// fork strips_media_from_json_string_and_nested_content：stringified JSON
// 形态——tool_result 的 content 是一个字符串，解析后内嵌 input_image。
var visionStringifiedJSONImage = `{"model":"m","messages":[{"role":"user","content":[` +
	`{"type":"tool_result","tool_use_id":"t1","content":` +
	visionJSONString(`{"content":[{"type":"input_text","text":"caption"},{"type":"input_image","image_url":"`+visionLargeImageDataURL+`"}]}`) +
	`}]}]}`

// visionNestedJSONImage 把一个带 payload 的 input_image 包进 n 层数组的
// JSON 文本，整体作为 tool_result 的 content 字符串。字符串分支解析成功
// 后从 depth+1 起算，所以 part 的 depth 是 n+1——31 层命中，32 层过
// MAX_MEDIA_TRAVERSAL_DEPTH=32 的上限。
func visionNestedJSONImage(n int) string {
	inner := `{"type":"input_image","image_url":"https://example.com/a.png"}`
	return `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` +
		visionJSONString(strings.Repeat(`[`, n)+inner+strings.Repeat(`]`, n)) + `}]}]}`
}

// visionNestedContentChain 把图片 part 包进 n 层 {"content":[…]} 的
// object 链：数组里的 block 靠 block.content 递归（宽松路径），这条链
// 没有深度上限——40 层仍命中，与严格判定的 32 层上限相区别。
func visionNestedContentChain(n int) string {
	inner := `{"type":"input_image"}`
	s := strings.Repeat(`{"content":[`, n) + inner + strings.Repeat(`]}`, n)
	return `{"model":"m","messages":[{"role":"user","content":[` + s + `]}]}`
}

// visionJSONString 把 s 编码成一个 JSON 字符串字面量（带引号）。
func visionJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// visionModelOf 以 to 为目标模型跑一次改写，给出 body 里的 model；ok 说
// 它是否被改写（未触发时字节原样返回）。
func visionModelOf(t *testing.T, to, body string) (model string, rewritten bool) {
	t.Helper()
	got := visionRouteBody(to, []byte(body))
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
	// want 是 "" 时请求必须一个字节都不改（fork：未命中时行为与没有
	// 视觉路由一致）；否则是改写后的 model。
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		// ---- 命中：fork 的三种到图形态 ----
		{"tool_result 内嵌图片，改写", visionToolResultImage, "vision-model"},
		{"尾部 assistant 跳过，往前找到带图 user", visionTrailingAssistant, "vision-model"},
		{"最新 user 的 image block，改写", visionLatestImageBlock, "vision-model"},
		{"双轮时序轮 2：Read 读回图片", visionImagePathRound2, "vision-model"},
		// 外层判定只看 block.type（fork is_image_block_type），无 payload 也命中
		{"user content 里无 payload 的 input_image", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"input_image"}]}]}`, "vision-model"},
		{"user content 里无 payload 的 image_url", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`, "vision-model"},
		// ---- tool_output 判定（tool_result.content 内，严格形态）----
		{"stringified JSON 里内嵌 input_image", visionStringifiedJSONImage, "vision-model"},
		{"tool_result content 整体是 8KB data URL", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + visionLargeImageDataURL + `"}]}]}`, "vision-model"},
		{"stringified 嵌套 31 层（part 的 depth 恰为 32）内命中", visionNestedJSONImage(31), "vision-model"},
		{"stringified 嵌套 32 层过深度上限", visionNestedJSONImage(32), ""},
		// ---- 不命中 ----
		{"历史图片不触发", visionHistoryImage, ""},
		{"双轮时序轮 1：只有图片路径文本", visionImagePathRound1, ""},
		{"content 是字符串，不命中", `{"model":"m","messages":[{"role":"user","content":"看一下 ~/a.png 的内容"}]}`, ""},
		// tool_result.content 是数组时 fork 先走宽松递归（type-only）：
		// 无 payload 的 input_image 也命中
		{"tool_result content 数组里无 payload 的 input_image 命中", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"input_image"}]}]}]}`, "vision-model"},
		{"content 链 40 层仍命中（宽松递归无深度上限）", visionNestedContentChain(40), "vision-model"},
		// 字符串形态只剩严格判定（fork tool_media_kind 要 payload）
		{"stringified 无 payload 的 input_image，不命中", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + visionJSONString(`{"type":"input_image"}`) + `}]}]}`, ""},
		// data URL 的 8KB 门槛（fork WHOLE_DATA_URL_MIN_BYTES）
		{"小 data URL 不命中", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"data:image/png;base64,YWJj"}]}]}`, ""},
		// fork：body.model 必须是字符串才走 vision 分支
		{"无 model 字段的带图请求不改写", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}`, ""},
		// ---- 结构限定 ----
		{"Responses 形态不命中", visionResponsesShape, ""},
		{"Gemini 形态不命中", visionGeminiShape, ""},
		// ---- 意外输入静默不触发 ----
		{"messages 非数组", `{"model":"m","messages":"not an array"}`, ""},
		{"body 非对象", `[{"role":"user"}]`, ""},
		{"没有 user 消息", `{"model":"m","messages":[{"role":"assistant","content":[{"type":"image"}]}]}`, ""},
		{"user 消息 content 为 null", `{"model":"m","messages":[{"role":"user","content":null}]}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			model, rewritten := visionModelOf(t, "vision-model", c.body)
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

// 请求入口绑到一个组（body.model 以 "group/" 开头）时视觉路由不改写：
// 项目绑定组是「唯一出口」语义，改写到组外模型会击穿它（fork 原实现
// 无此问题——vision 在候选链每个 provider 身上各自生效，不换出口）。
// 守卫不误伤：非 group 入口（生产正文模型）带图照常改写。
func TestVisionRouteKeepsGroupBoundRequests(t *testing.T) {
	const groupBound = `{"model":"group/proj-1","messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}]}`
	model, rewritten := visionModelOf(t, "zhipu/glm-5.3-flash", groupBound)
	if rewritten || model != "group/proj-1" {
		t.Fatalf("绑组请求被改写成 %q（改写 %v）", model, rewritten)
	}
	const plain = `{"model":"zhipu/glm-5.3","messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}]}`
	model, rewritten = visionModelOf(t, "zhipu/glm-5.3-flash", plain)
	if !rewritten || model != "zhipu/glm-5.3-flash" {
		t.Fatalf("非组请求未改写，得 %q（改写 %v）", model, rewritten)
	}
}

// 视觉路由模型的三种写法（裸名、provider/model、group/<id>）都原样写进
// body.model，不在改写里解析——网关自己去认（fork 的配置语义照搬：包括
// 大写 "[1M]" 后缀也不纠正，窗口标记是小写 "[1m]"，TrimSuffix 大小写
// 敏感，大写后缀会原样发上游——纠正责任在配置文档）。
func TestVisionRouteTakesTheModelAsWritten(t *testing.T) {
	for _, m := range []string{
		"gpt-image",
		"openai/gpt-image",
		"group/vision",
		"zhipu/glm-5.3-flash",
		"zhipu/glm-5.3-flash[1M]",
	} {
		got, rewritten := visionModelOf(t, m, visionToolResultImage)
		if !rewritten || got != m {
			t.Fatalf("%s: got %q（改写 %v）", m, got, rewritten)
		}
	}
}

// 未配置视觉模型（空串、空白串）时 visionRoute 恒不触发——行为与没有
// 视觉路由一致。末尾的对照证明"原样"是判定为未配置：配上 model 后同一
// 请求被改写。
func TestVisionRouteNotConfiguredChangesNothing(t *testing.T) {
	for _, to := range []string{"", "   "} {
		got := visionRoute(provider.Anthropic, []byte(visionToolResultImage), to)
		if string(got) != visionToolResultImage {
			t.Fatalf("to %q: 带图请求被改写成 %s", to, got)
		}
	}
	got := visionRoute(provider.Anthropic, []byte(visionToolResultImage), "vision-model")
	if string(got) == visionToolResultImage {
		t.Fatalf("对照：配上 model 后应改写")
	}
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(got, &m) != nil || m.Model != "vision-model" {
		t.Fatalf("改写后 model=%q", m.Model)
	}
}

// 非Anthropic 协议直接返回：fork 有意把视觉路由限定在 Claude 侧
// （Responses input / Gemini contents 形态天然不命中）。
func TestVisionRouteOnlyOnAnthropicRequests(t *testing.T) {
	for _, p := range []provider.Protocol{provider.Responses, provider.Gemini, provider.Chat} {
		got := visionRoute(p, []byte(visionToolResultImage), "vision-model")
		if string(got) != visionToolResultImage {
			t.Fatalf("%s 协议的请求被改写：%s", p, got)
		}
	}
}

// 视觉路由（内置）从网关里过一遍：不装任何 JS 中间件，请求 model 是
// 裸名（fake/text-only）带图，路由决策前被改写到 settings 配的
// fake/m1，上游收到 m1；同一模型不带图的请求原样。usage.jsonl 的 model
// 列记改写后的（路由按它走）。
func TestBuiltInVisionRouteThroughGateway(t *testing.T) {
	fresh(t) // nothing another test left on "fake" (#3273)
	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b struct {
			Model string `json:"model"`
		}
		json.Unmarshal(raw, &b)
		mu.Lock()
		sent = append(sent, b.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"`+b.Model+`","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1", "text-only"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{VisionRouteModel: "fake/m1"}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	// 带 tool_result 图片的请求（fork model_mapper.rs 的原 fixture）：
	// 入口模型 text-only，改写到 fake/m1，上游收到 m1。
	if code, body := post(t, "/v1/messages", visionToolResultImage); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	// 同一入口模型、不带图：不改写，上游收到 text-only。
	if code, body := post(t, "/v1/messages", `{"model":"text-only","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("上游收到 %d 次请求：%q", len(sent), sent)
	}
	if sent[0] != "m1" {
		t.Fatalf("带图请求上游收到 model %q, want m1", sent[0])
	}
	if sent[1] != "text-only" {
		t.Fatalf("无图请求上游收到 model %q, want text-only", sent[1])
	}
	// 路由按改写后的 model 走：usage.jsonl 记的也是 m1，不是入口的
	// text-only（改写发生在路由决策前的证据）；无图请求照原文记。
	rows, text := loggedRows(t)
	if len(rows) != 2 {
		t.Fatalf("usage.jsonl 应记两行，得 %d：%s", len(rows), text)
	}
	if rows[0].Model != "m1" {
		t.Fatalf("带图请求的 usage model=%q, want m1（改写后）：%s", rows[0].Model, text)
	}
	if rows[1].Model != "text-only" {
		t.Fatalf("无图请求的 usage model=%q, want text-only（原样）", rows[1].Model)
	}
}
