package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// =====================================================================
// magpie project bind/unbind 与 magpie projects：把项目目录的 Claude
// Code 钉在一个路由组上。命令层测试走顶层 run（runOut），顺带盖住
// main.go 的 case 与 help 拦截。
// =====================================================================

// wantProjectPins 是 pin 文件的全部键（跨包硬编码：键清单是 agent 与 cli
// 之间的契约，两边各自写死才查得出谁单方面改了）。
var wantProjectPins = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_AUTO_MODE_MODEL",
	"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
}

// projectsHome 造 groupsHome 的哑 provider 沙箱 + 一个真组 + 一个带 .git
// 的项目目录，返回项目根。
func projectsHome(t *testing.T) string {
	t.Helper()
	groupsHome(t)
	if err := provider.SaveGroup(provider.Group{ID: "proj-x", Name: "Proj X", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// projectEnv 读项目 settings.local.json 的 env 对象。
func projectEnv(t *testing.T, root string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("settings.local.json unreadable: %v", err)
	}
	var f struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("settings.local.json is not JSON: %v (%s)", err, b)
	}
	return f.Env
}

// projectsTable 读 projects.json 的两个半边。
func projectsTable(t *testing.T) (bindings, snapshots map[string]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "projects.json"))
	if err != nil {
		t.Fatalf("projects.json unreadable: %v", err)
	}
	var f struct {
		Bindings  map[string]string `json:"bindings"`
		Snapshots map[string]string `json:"snapshots"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("projects.json unreadable: %v (%s)", err, b)
	}
	return f.Bindings, f.Snapshots
}

func TestProjectBindPinsAProjectToAGroup(t *testing.T) {
	root := projectsHome(t)
	out, err := runOut(t, "project", "bind", root, "proj-x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "group/proj-x") {
		t.Fatalf("bind output names no group ref:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(root, ".claude", "settings.local.json")) {
		t.Fatalf("bind output names no file path:\n%s", out)
	}
	m, err := agent.ProjectBindings()
	if err != nil || m[root] != "group/proj-x" {
		t.Fatalf("bindings = %v (err %v), want %s -> group/proj-x", m, err, root)
	}
	env := projectEnv(t, root)
	for _, k := range wantProjectPins {
		want := "group/proj-x"
		if k == "CLAUDE_CODE_MAX_CONTEXT_TOKENS" {
			want = "272000" // 成员无窗口 → magpie 默认工作窗口
		}
		if env[k] != want {
			t.Fatalf("env.%s = %v, want %v", k, env[k], want)
		}
	}
}

func TestProjectBindByGroupNameWorksToo(t *testing.T) {
	root := projectsHome(t)
	if _, err := runOut(t, "project", "bind", root, "Proj X"); err != nil {
		t.Fatal(err)
	}
	m, err := agent.ProjectBindings()
	if err != nil || m[root] != "group/proj-x" {
		t.Fatalf("bind by name = %v (err %v)", m, err)
	}
}

func TestProjectBindNamesItsGitRoot(t *testing.T) {
	root := projectsHome(t)
	sub := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, "project", "bind", sub, "proj-x"); err != nil {
		t.Fatal(err)
	}
	m, err := agent.ProjectBindings()
	if err != nil || m[root] != "group/proj-x" {
		t.Fatalf("bind by subdir keyed %v (err %v), want the git root %s", m, err, root)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("pins not at the git root: %v", err)
	}
}

func TestProjectBindAnUnknownGroupTeachesGroupAdd(t *testing.T) {
	root := projectsHome(t)
	_, err := runOut(t, "project", "bind", root, "nope")
	if err == nil || !strings.Contains(err.Error(), "magpie group add") {
		t.Fatalf("unknown group error = %v, want it to teach magpie group add", err)
	}
	if m, _ := agent.ProjectBindings(); len(m) != 0 {
		t.Fatalf("a failed bind left bindings behind: %v", m)
	}
}

func TestProjectUnbindClearsBindingAndPins(t *testing.T) {
	root := projectsHome(t)
	if _, err := runOut(t, "project", "bind", root, "proj-x"); err != nil {
		t.Fatal(err)
	}
	out, err := runOut(t, "project", "unbind", root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "group/proj-x") {
		t.Fatalf("unbind output names no group:\n%s", out)
	}
	bindings, snapshots := projectsTable(t)
	if _, ok := bindings[root]; ok {
		t.Fatal("the binding row survived unbind")
	}
	if snapshots[root] == "" {
		t.Fatal("the snapshot went with the row; the dual-map ruling keeps it as regret medicine")
	}
	env := projectEnv(t, root)
	for _, k := range wantProjectPins {
		if _, ok := env[k]; ok {
			t.Fatalf("env.%s survived unbind", k)
		}
	}
}

func TestProjectUnbindAPathNeverBoundIsAnError(t *testing.T) {
	root := projectsHome(t)
	if _, err := runOut(t, "project", "unbind", root); err == nil {
		t.Fatal("unbinding a path never bound said nothing")
	}
}

func TestProjectsListShowsBindingsAndSyncStates(t *testing.T) {
	root := projectsHome(t)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, "project", "bind", root, "proj-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, "project", "bind", other, "proj-x"); err != nil {
		t.Fatal(err)
	}
	// 把一个项目的 pin 改坏制造漂移
	if err := os.WriteFile(filepath.Join(other, ".claude", "settings.local.json"),
		[]byte(`{"env":{"ANTHROPIC_MODEL":"group/somewhere-else"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runOut(t, "projects")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		filepath.Base(root), filepath.Base(other), "group/proj-x", "in place", "out of sync",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("projects list misses %q:\n%s", want, out)
		}
	}
}

func TestProjectHelpAndStrayValues(t *testing.T) {
	root := projectsHome(t)
	out, err := runOut(t, "project", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "usage") || !strings.Contains(out, "magpie project bind") {
		t.Fatalf("project --help:\n%s", out)
	}
	if _, err := runOut(t, "project", "bind", root); err == nil {
		t.Fatal("bind without a group went ahead")
	}
	if _, err := runOut(t, "project", "frobnicate", root); err == nil {
		t.Fatal("an unknown subcommand went ahead")
	}
	if m, _ := agent.ProjectBindings(); len(m) != 0 {
		t.Fatalf("a refused bind left bindings behind: %v", m)
	}
}
