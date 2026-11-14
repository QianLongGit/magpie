package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// 视觉路由中间件从网关里过一遍：最新 user 消息带图（tool_result 形态）
// 的请求被改写到 options.model（provider/model 写法），网关把改写后的
// 请求送到该 provider；同一模型不带图的请求原样。脚本与判定矩阵测试
// 用的是同一份文件（internal/middleware/testdata），防两处漂移。
func TestVisionRouteMiddlewareThroughGateway(t *testing.T) {
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
	t.Cleanup(plugin.Settle)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1", "text-only"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "middleware", "testdata", "vision-route.middleware.js"))
	if err != nil {
		t.Fatal(err)
	}
	mw := filepath.Join(t.TempDir(), "vision-route.middleware.js")
	if err := os.WriteFile(mw, src, 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: mw, Options: map[string]any{"model": "fake/m1"}}}})
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	middleware.Reload()
	defer middleware.Reload()

	// 带 tool_result 图片的请求（fork model_mapper.rs 的原 fixture）：
	// 入口模型 text-only，中间件改写到 fake/m1，上游收到 m1。
	const withImage = `{"model":"fake/text-only","max_tokens":10,"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.png"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc"}}]}]}]}`
	if code, body := post(t, "/v1/messages", withImage); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	// 同一入口模型、不带图：不改写，上游收到 text-only。
	if code, body := post(t, "/v1/messages", `{"model":"fake/text-only","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
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
	if st := middleware.States()[mw]; st.Failures != 0 || st.Calls < 2 {
		t.Fatalf("state %+v", st)
	}
}
