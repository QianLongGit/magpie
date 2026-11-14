package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// sandboxProjects 给本组测试一个自己的 magpie 配置目录：绑定表
// projects.json 落在 settings.Dir() 下，绝不碰真实用户的。
func sandboxProjects(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return filepath.Join(settings.Dir(), "projects.json")
}

// =====================================================================
// 绑定表 projects.json：key = ProjectRoot 的规范化路径，value = 绑定行
// ProjectBinding{Group, Snapshot}——组 ref 与写前快照存在同一行里，快
// 照随绑定行删除（解绑/级联删行 = 快照同生命周期消失）。
// =====================================================================

// TestProjectBindingsSaveAndLoadBack: 存两条（含中文路径，一条带快照）
// 读回相等，文件落在 settings.Dir() 下。
func TestProjectBindingsSaveAndLoadBack(t *testing.T) {
	path := sandboxProjects(t)
	cn := filepath.Join(t.TempDir(), "中文项目")
	m := map[string]ProjectBinding{
		filepath.Join(t.TempDir(), "proj-a"): {Group: "group/proj-a"},
		cn:                                   {Group: "group/中文组", Snapshot: "原文"},
	}
	if err := SaveProjectBindings(m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("projects.json not written: %v", err)
	}
	got, err := ProjectBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(m) {
		t.Fatalf("read back %d bindings, want %d (%v)", len(got), len(m), got)
	}
	for k, v := range m {
		if got[k] != v {
			t.Fatalf("binding %q = %+v, want %+v", k, got[k], v)
		}
	}
}

// TestProjectBindingsBrokenFileReadsAsError: 读不出来的表是未知，不是
// 空——空 map 会让悬空对账以为没有绑定而跳过清理（failed read =
// unknown 红线）。
func TestProjectBindingsBrokenFileReadsAsError(t *testing.T) {
	path := sandboxProjects(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, err := ProjectBindings(); err == nil {
		t.Fatalf("a broken bindings file read as %v, want an error", m)
	}
}

// TestProjectBindingsUnparsableKeptAside: 写前把不可解析的原文旁存
// （.bad-<time>），绝不覆盖吞掉。
func TestProjectBindingsUnparsableKeptAside(t *testing.T) {
	path := sandboxProjects(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := map[string]ProjectBinding{"/proj": {Group: "group/x"}}
	if err := SaveProjectBindings(m); err != nil {
		t.Fatal(err)
	}
	kept, err := filepath.Glob(path + ".bad-*")
	if err != nil || len(kept) == 0 {
		t.Fatalf("the unparsable file was not kept aside (glob %v, err %v)", kept, err)
	}
	if b, err := os.ReadFile(kept[0]); err != nil || string(b) != "{not json" {
		t.Fatalf("kept-aside copy = %q (err %v), want the original bytes", b, err)
	}
	got, err := ProjectBindings()
	if err != nil || got["/proj"].Group != "group/x" {
		t.Fatalf("after save, read back %v (err %v), want the saved binding", got, err)
	}
}

// TestProjectBindingsMissingFileReadsAsNone: 没有文件 = 没有绑定，非错误。
func TestProjectBindingsMissingFileReadsAsNone(t *testing.T) {
	sandboxProjects(t)
	m, err := ProjectBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("missing file read as %v, want none", m)
	}
}

// TestProjectBindingsSaveDropsARowWithItsSnapshot: 删一条绑定行 = 该行
// 的快照同生命周期消失（row-delete 终裁）。文件直读断言：被删的 root
// 整行不在了，留下的行其快照还在。
func TestProjectBindingsSaveDropsARowWithItsSnapshot(t *testing.T) {
	path := sandboxProjects(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"/a":{"group":"group/x","snapshot":"原文 A"},"/b":{"group":"group/y","snapshot":"原文 B"}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// 只剩 /b 一条绑定：/a 的行被删，它的快照必须随行消失
	if err := SaveProjectBindings(map[string]ProjectBinding{"/b": {Group: "group/y", Snapshot: "原文 B"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]ProjectBinding
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("saved file is not a binding table: %v (%s)", err, b)
	}
	if _, ok := f["/a"]; ok {
		t.Fatalf("row /a survived the save: %v, want it and its snapshot gone", f)
	}
	if f["/b"].Group != "group/y" || f["/b"].Snapshot != "原文 B" {
		t.Fatalf("row /b = %+v, want its group and snapshot kept", f["/b"])
	}
	if strings.Contains(string(b), "原文 A") {
		t.Fatalf("the dropped row's snapshot outlived it: %s", b)
	}
}

// TestProjectBindingsReadARowWithItsSnapshot: 快照是绑定行的字段（schema
// 事实）：行在，Group 与 Snapshot 一起读回。
func TestProjectBindingsReadARowWithItsSnapshot(t *testing.T) {
	path := sandboxProjects(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"/p":{"group":"group/x","snapshot":"写前原文"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ProjectBindings()
	if err != nil {
		t.Fatal(err)
	}
	if m["/p"].Group != "group/x" || m["/p"].Snapshot != "写前原文" {
		t.Fatalf("row read back %+v, want group group/x with its snapshot", m["/p"])
	}
}

// =====================================================================
// pin 写入器 WriteProjectPins：把 root/.claude/settings.local.json 的
// env 钉在一个组 ref 上。整组键一起写、一起清——残留半组 pin（tier 行
// 为不一致）比不 pin 更糟。窗口键的值与 magpie 接线同一惯例（字符串）。
// =====================================================================

// wantPinEnv 是全部模型 pin 键（硬编码进测试：实现漏掉任何一个这里都
// 必须红，不许从实现的 projectPinEnv 读——那会变成同义反复）。
var wantPinEnv = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_AUTO_MODE_MODEL",
}

// pinProject 造一个带 .git 的沙箱项目并独立绑定表，返回项目根。
func pinProject(t *testing.T) string {
	t.Helper()
	sandboxProjects(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// readProjectsTable 读回 projects.json 的绑定表（同包直读，schema 即契
// 约：map[root]ProjectBinding）。
func readProjectsTable(t *testing.T) map[string]ProjectBinding {
	t.Helper()
	b, err := os.ReadFile(projectsPath())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]ProjectBinding
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("projects.json unreadable: %v (%s)", err, b)
	}
	return m
}

// readLocalEnv 解析项目的 settings.local.json，返回 env 对象。
func readLocalEnv(t *testing.T, root string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(localSettingsPath(root))
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

// TestPinsCreateMinimalStructureWhenFileMissing: 文件不存在时建最小结
// 构，绑定行建立，无原文可快照则 Snapshot 为空。
func TestPinsCreateMinimalStructureWhenFileMissing(t *testing.T) {
	root := pinProject(t)
	if err := WriteProjectPins(root, "group/proj-x"); err != nil {
		t.Fatal(err)
	}
	env := readLocalEnv(t, root)
	for _, k := range wantPinEnv {
		if env[k] != "group/proj-x" {
			t.Fatalf("env.%s = %v, want group/proj-x", k, env[k])
		}
	}
	if env[claudeContextEnv] != strconv.Itoa(settings.WorkingWindow) {
		t.Fatalf("env.%s = %v, want %d (fallback window)", claudeContextEnv, env[claudeContextEnv], settings.WorkingWindow)
	}
	f := readProjectsTable(t)
	if f[root].Group != "group/proj-x" {
		t.Fatalf("binding row = %+v, want group/proj-x", f[root])
	}
	if f[root].Snapshot != "" {
		t.Fatalf("snapshot = %q, want none (no file was there)", f[root].Snapshot)
	}
}

// TestPinsKeepUsersOtherEnvAndTopLevelKeys: 用户自己的 env 键与顶层键
// 原样保留（fork set_preserves_user_other_headers 对应）。
func TestPinsKeepUsersOtherEnvAndTopLevelKeys(t *testing.T) {
	root := pinProject(t)
	p := localSettingsPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"env":{"ANTHROPIC_API_KEY":"k"},"permissions":{"allow":["Bash(ls:*)"]}}`
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/proj-x"); err != nil {
		t.Fatal(err)
	}
	env := readLocalEnv(t, root)
	if env["ANTHROPIC_API_KEY"] != "k" {
		t.Fatalf("user's own env key lost: %v", env)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Permissions map[string]any `json:"permissions"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("file is not JSON after pins: %v (%s)", err, b)
	}
	allow, _ := f.Permissions["allow"].([]any)
	if len(allow) != 1 || allow[0] != "Bash(ls:*)" {
		t.Fatalf("permissions changed: %s", b)
	}
}

// TestPinsRebindingRollsSnapshot: 每次写前滚动单份快照存进行内（fork
// set_backs_up_existing_file_once_per_write 的 REPLACE 语义）——同项目
// rebind 用当前文件内容覆盖该行 Snapshot。
func TestPinsRebindingRollsSnapshot(t *testing.T) {
	root := pinProject(t)
	p := localSettingsPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	p1 := `{"env":{"ANTHROPIC_API_KEY":"one"}}`
	if err := os.WriteFile(p, []byte(p1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/a"); err != nil {
		t.Fatal(err)
	}
	if got := readProjectsTable(t)[root].Snapshot; got != p1 {
		t.Fatalf("snapshot after first bind = %q, want %q", got, p1)
	}
	p2 := `{"env":{"ANTHROPIC_API_KEY":"two"},"model":"x"}`
	if err := os.WriteFile(p, []byte(p2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/a"); err != nil {
		t.Fatal(err)
	}
	if got := readProjectsTable(t)[root].Snapshot; got != p2 {
		t.Fatalf("snapshot after rebind = %q, want %q (rolled)", got, p2)
	}
}

// TestPinsCorruptFileSnapshottedThenRebuilt: 损坏 JSON 先快照后按最小
// 结构重建（fork set_rebuilds_corrupted_json 同形）。
func TestPinsCorruptFileSnapshottedThenRebuilt(t *testing.T) {
	root := pinProject(t)
	p := localSettingsPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not json{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/proj-x"); err != nil {
		t.Fatal(err)
	}
	if got := readProjectsTable(t)[root].Snapshot; got != "not json{" {
		t.Fatalf("corrupt original not snapshotted: %q", got)
	}
	env := readLocalEnv(t, root)
	if len(env) != len(wantPinEnv)+1 {
		t.Fatalf("rebuilt file holds %d env keys (%v), want only the %d pins + window",
			len(env), env, len(wantPinEnv)+1)
	}
}

// TestPinsAWriteFailureKeepsTheRow: 文件写失败（.claude 写保护）→ 报
// 错但绑定行保留——「重跑 bind 即修复」的前提（fork
// write_failure_keeps_db_row）；恢复后重跑一遍即行与文件都在。
func TestPinsAWriteFailureKeepsTheRow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0500 is not a write block on Windows")
	}
	root := pinProject(t)
	dir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.Chmod(dir, 0o0500); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/proj-w"); err == nil {
		t.Fatal("a write-protected .claude let the pins through")
	}
	if got := readProjectsTable(t)[root].Group; got != "group/proj-w" {
		t.Fatalf("a failed file write left no row (%q): re-running bind could not repair", got)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/proj-w"); err != nil {
		t.Fatal(err)
	}
	if got := readProjectsTable(t)[root].Group; got != "group/proj-w" {
		t.Fatalf("row after the re-run = %q, want group/proj-w", got)
	}
	env := readLocalEnv(t, root)
	for _, k := range wantPinEnv {
		if env[k] != "group/proj-w" {
			t.Fatalf("env.%s = %v after the re-run, want group/proj-w", k, env[k])
		}
	}
}

// TestPinsEveryKeyWrittenNoneLeftOut: 红线测试——逐一断言整组键完整，
// 实现漏写任何一个（或写成别的值）都必须红。
func TestPinsEveryKeyWrittenNoneLeftOut(t *testing.T) {
	root := pinProject(t)
	if err := WriteProjectPins(root, "group/proj-x"); err != nil {
		t.Fatal(err)
	}
	env := readLocalEnv(t, root)
	seen := map[string]bool{}
	for k, v := range env {
		seen[k] = true
		if v != "group/proj-x" && k != claudeContextEnv {
			t.Fatalf("env.%s = %v, unexpected", k, v)
		}
	}
	for _, k := range wantPinEnv {
		if !seen[k] {
			t.Fatalf("env.%s missing from %v", k, env)
		}
	}
	if !seen[claudeContextEnv] {
		t.Fatalf("env.%s (window) missing from %v", claudeContextEnv, env)
	}
}

// =====================================================================
// pin 清除与同步状态：清除覆盖全部 pin 键（模型 8 键 + 窗口键）——残留
// 半组 pin 比 不 pin 更糟；同步状态只看模型键（窗口值随组配置变化，非
// 漂移信号）。
// =====================================================================

// TestClearDropsEveryPinKeyKeepsTheRest: 红线测试——拔除必须覆盖整组键，
// 实现漏拔任何一个都必须红；用户的 env 键与顶层键原样，写前滚动快照。
func TestClearDropsEveryPinKeyKeepsTheRest(t *testing.T) {
	root := pinProject(t)
	if err := WriteProjectPins(root, "group/proj-x"); err != nil {
		t.Fatal(err)
	}
	p := localSettingsPath(root)
	if err := edit.SetJSON(p,
		edit.KV{Path: "env.ANTHROPIC_API_KEY", Value: "user-key"},
		edit.KV{Path: "permissions.allow", Value: []string{"Bash(ls:*)"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := ClearProjectPins(root); err != nil {
		t.Fatal(err)
	}
	env := readLocalEnv(t, root)
	for _, k := range append(append([]string{}, wantPinEnv...), claudeContextEnv) {
		if v, ok := env[k]; ok {
			t.Fatalf("env.%s = %v after clear, want gone", k, v)
		}
	}
	if env["ANTHROPIC_API_KEY"] != "user-key" {
		t.Fatalf("user's own env key lost: %v", env)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "Bash(ls:*)") {
		t.Fatalf("permissions lost after clear: %s", after)
	}
	// 写前滚动快照存进行内：清的是「全 pin + 用户键」的原文
	if got := readProjectsTable(t)[root].Snapshot; !strings.Contains(got, "user-key") {
		t.Fatalf("pre-clear snapshot = %q, want the file as it was (with user-key)", got)
	}
}

// TestClearOkWhenFileMissing: 文件不存在 → nil（幂等）。
func TestClearOkWhenFileMissing(t *testing.T) {
	root := pinProject(t)
	if err := ClearProjectPins(root); err != nil {
		t.Fatalf("clear on missing file = %v, want nil", err)
	}
}

// TestClearLeavesAPinlessFileAlone: 没有 pin 键的文件逐字节不动。
func TestClearLeavesAPinlessFileAlone(t *testing.T) {
	root := pinProject(t)
	p := localSettingsPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "{\n  \"env\": {\n    \"ANTHROPIC_API_KEY\": \"k\"\n  }\n}\n"
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ClearProjectPins(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != orig {
		t.Fatalf("pinless file changed:\n%s\nwant:\n%s", after, orig)
	}
}

// TestSyncSaysPinsPresentAndMatching: 全 8 键在且 = ref → Synced（窗口
// 值不参与判定）。
func TestSyncSaysPinsPresentAndMatching(t *testing.T) {
	root := pinProject(t)
	if err := WriteProjectPins(root, "group/proj-x"); err != nil {
		t.Fatal(err)
	}
	// 窗口值改掉也不算漂移
	if err := edit.SetJSON(localSettingsPath(root), edit.KV{Path: "env." + claudeContextEnv, Value: "999"}); err != nil {
		t.Fatal(err)
	}
	if got := ProjectSync(root, "group/proj-x"); got != ProjectSynced {
		t.Fatalf("ProjectSync = %v, want ProjectSynced", got)
	}
}

// TestSyncCatchesMissingPinMismatchedValueAndMissingFile: 三态矩阵——缺
// 一个键、值不对、文件缺失都报 OutOfSync。
func TestSyncCatchesMissingPinMismatchedValueAndMissingFile(t *testing.T) {
	// 缺一个键
	root := pinProject(t)
	if err := WriteProjectPins(root, "group/a"); err != nil {
		t.Fatal(err)
	}
	if err := edit.DelJSON(localSettingsPath(root), "env."+wantPinEnv[3]); err != nil {
		t.Fatal(err)
	}
	if got := ProjectSync(root, "group/a"); got != ProjectOutOfSync {
		t.Fatalf("a missing pin read %v, want OutOfSync", got)
	}
	// 值不对（换了组没重绑）
	root2 := pinProject(t)
	if err := WriteProjectPins(root2, "group/a"); err != nil {
		t.Fatal(err)
	}
	if got := ProjectSync(root2, "group/b"); got != ProjectOutOfSync {
		t.Fatalf("a mismatched value read %v, want OutOfSync", got)
	}
	// 文件没了
	root3 := pinProject(t)
	if err := WriteProjectPins(root3, "group/a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(localSettingsPath(root3)); err != nil {
		t.Fatal(err)
	}
	if got := ProjectSync(root3, "group/a"); got != ProjectOutOfSync {
		t.Fatalf("a missing file read %v, want OutOfSync", got)
	}
}

// =====================================================================
// 项目根定位：ProjectRoot 把一个目录归到它所属 git 仓库（worktree）的
// 顶层。它是绑定表 projects.json 的 key，也是 .claude/settings.local.json
// 的父目录——与 Claude Code v2.1.211+ 在仓库根读写 local 文件的行为对齐。
// =====================================================================

// TestProjectRootIsTheGitWorktreeTopFolder: proj/.git 是目录时，深处的
// 子目录归到 proj 本身。
func TestProjectRootIsTheGitWorktreeTopFolder(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(proj, "sub", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot(deep); got != proj {
		t.Fatalf("ProjectRoot(%q) = %q, want %q", deep, got, proj)
	}
}

// TestProjectRootDotGitFileRootsTheWorktreeItself: git worktree 的 .git 是
// 文件（gitdir: 形态）。根定位只把它当「这里有仓库」的信号，不解析其
// 内容——顶层就是 .git 文件所在的目录。
func TestProjectRootDotGitFileRootsTheWorktreeItself(t *testing.T) {
	base := t.TempDir()
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(wt, "sub")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot(deep); got != wt {
		t.Fatalf("ProjectRoot(%q) = %q, want %q", deep, got, wt)
	}
}

// TestProjectRootCorruptDotGitFileStillRootsTheFolder: 损坏的 .git 文件
// （内容非 gitdir: 形态）仍停在该目录。与 library 的 gitOf 有意不同：
// gitOf 会跳过损坏文件继续向上找，这里停——绑定只认「用户认为是仓库
// 根」的那一层，向上多走会把别的主仓库当成本项目的根。
func TestProjectRootCorruptDotGitFileStillRootsTheFolder(t *testing.T) {
	base := t.TempDir()
	// 上层伪装一个真仓库，精简实现不该走上去
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".git"), []byte("not a gitdir line at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot(proj); got != proj {
		t.Fatalf("ProjectRoot(%q) = %q, want %q", proj, got, proj)
	}
}

// TestProjectRootWithoutGitIsTheDirectoryItself: 没有 .git 时，目录自身
// 就是根（返回 Abs+EvalSymlinks 后的输入）。
func TestProjectRootWithoutGitIsTheDirectoryItself(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "plain", "nested")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot(dir); got != want {
		t.Fatalf("ProjectRoot(%q) = %q, want %q", dir, got, want)
	}
}

// TestProjectRootTwoSymlinkPathsLandOnOneRoot: 同一项目经 symlink 与原
// 路径各调用一次，归一到同一根（绑定表才不会出现两行）。
func TestProjectRootTwoSymlinkPathsLandOnOneRoot(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectRoot(link), ProjectRoot(proj); got != want {
		t.Fatalf("ProjectRoot(%q) = %q, want %q (via %q)", link, got, want, proj)
	}
}

// TestProjectRootMakesARelativePathAbsolute: 相对路径先转绝对再找根。
func TestProjectRootMakesARelativePathAbsolute(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(proj, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	got := ProjectRoot(filepath.Join("proj", "sub"))
	if !filepath.IsAbs(got) {
		t.Fatalf("ProjectRoot(relative) = %q, want absolute", got)
	}
	if want := ProjectRoot(proj); got != want {
		t.Fatalf("ProjectRoot(relative) = %q, want %q", got, want)
	}
}

// TestProjectRootNormalizesTheWindowsDriveLetter: 盘符大小写归一为纯
// 函数 normalizeVolume，跨平台可测（手工识别 X: 前缀，Linux 上同样生效）。
func TestProjectRootNormalizesTheWindowsDriveLetter(t *testing.T) {
	if got, want := normalizeVolume("c:\\x\\y"), "C:\\x\\y"; got != want {
		t.Fatalf("normalizeVolume(\"c:\\\\x\\\\y\") = %q, want %q", got, want)
	}
	if got, want := normalizeVolume("C:\\x\\y"), "C:\\x\\y"; got != want {
		t.Fatalf("normalizeVolume(\"C:\\\\x\\\\y\") = %q, want %q", got, want)
	}
	if got, want := normalizeVolume("/home/u/proj"), "/home/u/proj"; got != want {
		t.Fatalf("normalizeVolume(linux path) = %q, want %q", got, want)
	}
}

// =====================================================================
// 组变化级联：provider 的组写入单点（store 成功后）fire GroupsChanged，
// agent 侧 DropGoneProjectBindings 把悬空绑定清掉。次序红线：先清 pin
// （幂等），成功后才删绑定行；清 pin 失败留行待下次重试。删行 = 整条
// ProjectBinding 删，快照随行走。
// =====================================================================

// realGroupProject 沙箱里造「真组 + 项目绑定 + pin 文件」的完整状态，
// 返回项目根。成员挂在哑 chat 端点的 provider 上（never dialed）。
func realGroupProject(t *testing.T, id string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := provider.Save(provider.Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: id, Name: id, Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, "group/"+id); err != nil {
		t.Fatal(err)
	}
	return root
}

// goneAsserts 断言 root 的绑定行整条已删（快照随行消失，无残留）、pin
// 键全无而用户键原样。
func goneAsserts(t *testing.T, root string) {
	t.Helper()
	f := readProjectsTable(t)
	if _, ok := f[root]; ok {
		t.Fatalf("binding row for %s still there after its group is gone: %+v", root, f[root])
	}
	env := readLocalEnv(t, root)
	for _, k := range append(append([]string{}, wantPinEnv...), claudeContextEnv) {
		if _, ok := env[k]; ok {
			t.Fatalf("env.%s survived the cascade", k)
		}
	}
	if env["ANTHROPIC_API_KEY"] != "user-key" {
		t.Fatalf("user's own env key lost in the cascade: %v", env)
	}
}

// TestGoneDeletingAGroupDropsItsProjectBindings: 自建组真删 → 级联整行
// 删（快照随之）、清 pin（用户其他键原样）。
func TestGoneDeletingAGroupDropsItsProjectBindings(t *testing.T) {
	root := realGroupProject(t, "proj-x")
	if err := edit.SetJSON(localSettingsPath(root), edit.KV{Path: "env.ANTHROPIC_API_KEY", Value: "user-key"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.DeleteGroup("proj-x"); err != nil {
		t.Fatal(err)
	}
	goneAsserts(t, root)
}

// TestGoneHidingAGroupDoesTheSame: 找到的组（auto）rm 后是 hidden 而非
// 真删——同样悬空、同样级联（SaveGroup 会剥 Hidden，隐藏走 DeleteGroup
// 于 found 组的路径，照 groupfound_test 的造法：两 provider 同名模型）。
func TestGoneHidingAGroupDoesTheSame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	ref := ""
	for _, g := range provider.Groups() {
		if g.ID == "auto-m" && !g.Hidden {
			ref = g.Ref()
		}
	}
	if ref == "" {
		t.Fatal("no found group auto-m to bind to")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectPins(root, ref); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(localSettingsPath(root), edit.KV{Path: "env.ANTHROPIC_API_KEY", Value: "user-key"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	goneAsserts(t, root)
}

// TestGoneALiveGroupsBindingStays: 组还在（清单可服务）→ 不动。同时是计
// 划风险#1 的探针：sharedModels("claude") 必须列出真组（按 ID==ref），
// 否则活组绑定会被误清。
func TestGoneALiveGroupsBindingStays(t *testing.T) {
	root := realGroupProject(t, "proj-live")
	if dropped := DropGoneProjectBindings(); len(dropped) != 0 {
		t.Fatalf("a live group's binding dropped: %v", dropped)
	}
	if got := readProjectsTable(t)[root].Group; got != "group/proj-live" {
		t.Fatalf("live binding = %q, want group/proj-live", got)
	}
	if s := ProjectSync(root, "group/proj-live"); s != ProjectSynced {
		t.Fatalf("live project pins = %v, want ProjectSynced", s)
	}
}

// TestGoneAFailedPinClearKeepsTheBinding: 清 pin 失败（写保护）→ 行留、
// dropped 空；恢复后重跑 → 行删、pin 净（次序红线：先清 pin 后删行）。
func TestGoneAFailedPinClearKeepsTheBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0500 is not a write block on Windows")
	}
	root := realGroupProject(t, "proj-f")
	dir := filepath.Join(root, ".claude")
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.Chmod(dir, 0o0500); err != nil {
		t.Fatal(err)
	}
	if err := provider.DeleteGroup("proj-f"); err != nil {
		t.Fatal(err)
	}
	row := readProjectsTable(t)[root]
	if row.Group == "" {
		t.Fatal("a failed pin clear dropped the row: dead pins would stay forever, unseen by the row-driven reconcile")
	}
	// 清理失败留行的场合，快照随行留（Clear 写前快照进了行，行没删）
	if row.Snapshot == "" {
		t.Fatal("the kept row lost its snapshot while it stayed")
	}
	if dropped := DropGoneProjectBindings(); len(dropped) != 0 {
		t.Fatalf("dropped %v against a write-protected project", dropped)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if dropped := DropGoneProjectBindings(); len(dropped) != 1 || dropped[0] != root {
		t.Fatalf("retry dropped %v, want [%s]", dropped, root)
	}
	if _, ok := readProjectsTable(t)[root]; ok {
		t.Fatal("row still there after a clean retry")
	}
	env := readLocalEnv(t, root)
	for _, k := range append(append([]string{}, wantPinEnv...), claudeContextEnv) {
		if _, ok := env[k]; ok {
			t.Fatalf("env.%s survived the retry", k)
		}
	}
}
