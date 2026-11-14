package agent

import (
	"os"
	"path/filepath"
	"testing"
)

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
