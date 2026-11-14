package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/settings"
)

// =====================================================================
// 按项目路由（project routing）：把一个项目目录的 Claude Code 流量钉
// 在一个 magpie 路由组上——`magpie project bind` 把项目 .claude/ 下
// settings.local.json 的 env 全部指到 group/<id>。本文件先回答「项目
// 是哪一格」：根定位。
//
// 根定位与 Claude Code v2.1.211+ 的行为对齐：它在仓库根读写 local 文
// 件，所以 magpie 也以 git worktree 的顶层为「项目」。向上找第一个
// .git（目录或文件都算）即停——.git 文件不解析其 gitdir 内容，只需要
// 「这里有仓库」这个信号。与 library 的 gitOf 的已知差异：损坏的 .git
// 文件（内容非 gitdir: 形态）在这里也停在该目录、不继续向上（gitOf 会
// 跳过它继续找）——知情取舍：绑定只认用户认作仓库根的那一层，向上多
// 走会把别的主仓库当成本项目的根。
// =====================================================================

// ProjectRoot 是 p 所在项目的根：从 p 的绝对真实路径向上找第一个 .git
// （worktree 场景 .git 是文件，同样算），没有 .git 时用 p 本身。返回值
// 经符号链接解析、盘符大小写归一，可直接作绑定表的 key 与
// .claude/settings.local.json 的父目录。
func ProjectRoot(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		a = p
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		a = r
	}
	for d := a; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return normalizeVolume(d)
		}
		up := filepath.Dir(d)
		if up == d {
			// 到了文件系统顶也没有 .git：p 自己就是根
			return normalizeVolume(a)
		}
		d = up
	}
}

// normalizeVolume 把 Windows 盘符归一为大写（c:\x 与 C:\x 同一项目同
// 一行）。盘符靠手工识别而非 filepath.VolumeName——后者在其他系统恒
// 返回空串，而这一步要跨平台可测；无盘符的路径原样返回。
func normalizeVolume(p string) string {
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return strings.ToUpper(p[:2]) + p[2:]
	}
	return p
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// =====================================================================
// 绑定表 projects.json（settings.Dir() 下，先例 library.json /
// stash.json）：key 是 ProjectRoot 的规范化路径，value 是组的完整 ref
// （group/<id>，与 usage 归因、pin 值同一口径）。
//
// 快照在另一半 map 里（root -> 写前 settings.local.json 原文），与绑定
// 行解耦（决策日志 2026-11-14 终裁，对齐 fork 备份表 remove 后留存）：
// 删绑定行保快照、Save 只写 bindings 半边、同项目 rebind 滚动覆盖；没
// 有绑定行的快照（解绑后的后悔药）是已知可控形态。
// =====================================================================

// projectFile 是 projects.json 的两个半边。
type projectFile struct {
	Bindings  map[string]string `json:"bindings"`
	Snapshots map[string]string `json:"snapshots"`
}

// projectsPath 是绑定表文件的位置。
func projectsPath() string {
	return filepath.Join(settings.Dir(), "projects.json")
}

// ProjectBindings 读绑定表。读不出来的表是未知，不是空：空 map 会让悬
// 空对账以为没有绑定而跳过清理（failed read = unknown）。
func ProjectBindings() (map[string]string, error) {
	b, err := os.ReadFile(projectsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f projectFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", projectsPath(), err)
	}
	if f.Bindings == nil {
		f.Bindings = map[string]string{}
	}
	return f.Bindings, nil
}

// SaveProjectBindings 写绑定半边，快照半边原样带走（删行保快照）。
func SaveProjectBindings(m map[string]string) error {
	return writeProjects(func(f *projectFile) { f.Bindings = m })
}

// writeProjects 改 projects.json 的一个半边后整体写回：文件不可解析时
// 先 lastgood 旁存原文再按空表起步，绝不覆盖吞掉；写经 edit.WriteAtomic
// （temp+rename，同字节不写）。
func writeProjects(change func(*projectFile)) error {
	p := projectsPath()
	var f projectFile
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &f); err != nil {
			if kerr := lastgood.KeepUnparsed(p, lastgood.Into[projectFile]); kerr != nil {
				return kerr
			}
			f = projectFile{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	change(&f)
	if f.Bindings == nil {
		f.Bindings = map[string]string{}
	}
	if f.Snapshots == nil {
		f.Snapshots = map[string]string{}
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(p, append(out, '\n'))
}
