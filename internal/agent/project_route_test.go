package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
// 绑定表 projects.json：key = ProjectRoot 的规范化路径，value = 组的
// 完整 ref（group/<id>）。快照是另一半 map（root -> 写前原文），与绑定
// 行解耦：删绑定行保快照，Save 只写 bindings 半边。
// =====================================================================

// TestProjectBindingsSaveAndLoadBack: 存两条（含中文路径）读回相等，
// 文件落在 settings.Dir() 下。
func TestProjectBindingsSaveAndLoadBack(t *testing.T) {
	path := sandboxProjects(t)
	cn := filepath.Join(t.TempDir(), "中文项目")
	m := map[string]string{
		filepath.Join(t.TempDir(), "proj-a"): "group/proj-a",
		cn:                                   "group/中文组",
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
			t.Fatalf("binding %q = %q, want %q", k, got[k], v)
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
	m := map[string]string{"/proj": "group/x"}
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
	if err != nil || got["/proj"] != "group/x" {
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

// TestProjectBindingsSaveKeepsSnapshots: Save 只写 bindings 半边——删掉
// 一条绑定行，快照半边原样保留（删行保快照，双 map 解耦）。
func TestProjectBindingsSaveKeepsSnapshots(t *testing.T) {
	path := sandboxProjects(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"bindings":{"/a":"group/x","/b":"group/y"},"snapshots":{"/a":"原文 A","/b":"原文 B"}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// 只剩 /b 一条绑定：/a 的行被删，它的快照必须还在
	if err := SaveProjectBindings(map[string]string{"/b": "group/y"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Bindings  map[string]string `json:"bindings"`
		Snapshots map[string]string `json:"snapshots"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("saved file is not JSON: %v (%s)", err, b)
	}
	if f.Bindings["/a"] != "" || f.Bindings["/b"] != "group/y" {
		t.Fatalf("bindings after save = %v, want only /b", f.Bindings)
	}
	if f.Snapshots["/a"] != "原文 A" || f.Snapshots["/b"] != "原文 B" {
		t.Fatalf("snapshots after save = %v, want both kept", f.Snapshots)
	}
}

// TestProjectBindingsOrphanSnapshotReadsFine: 只有快照没有绑定的文件是
// 已知可控形态（解绑后的后悔药），读取不报错。
func TestProjectBindingsOrphanSnapshotReadsFine(t *testing.T) {
	path := sandboxProjects(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"snapshots":{"/gone":"旧文"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ProjectBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("orphan-snapshot file read as bindings %v, want none", m)
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
