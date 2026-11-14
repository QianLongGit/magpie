package cli

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/yetone/magpie/internal/agent"
)

// =====================================================================
// 按项目路由：一个项目目录的 Claude Code 流量钉在一个 magpie 路由组
// 上（cc-switch fork 的 project routing，magpie 侧零核心移植——不动
// gateway 管线，pin 写在项目自己的 .claude/settings.local.json 里）。
// 组是 Routing 域一等公民：先 magpie group add，再 bind；组删了，级联
// 自动解绑（agent.DropGoneProjectBindings）。
// =====================================================================

const projectUsage = `usage:
  magpie project bind <path> <group>    pin the project at path (its git worktree's top) to one routing
                                        group: the group's ref group/<id> goes into the project's own
                                        .claude/settings.local.json, every model Claude Code picks
                                        through it; make the group first (magpie group add)
  magpie project unbind <path>          take the pins back out and forget the binding: the whole row goes,
                                        its pre-bind snapshot with it
  magpie projects                       the bound projects, and whether each one's pins are still in
                                        place (a group gone takes its bindings with it)
`

// projectCmd 是 magpie project 的子命令。--help 由 run 统一拦截，这里
// 只按 LESSONS「命令不吃 flag/杂值当数据」拒绝错形态。
func projectCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", projectUsage)
	}
	verb, rest := args[1], args[2:]
	switch verb {
	case "bind":
		if len(rest) != 2 {
			return fmt.Errorf("magpie project bind <path> <group>")
		}
		return projectBind(rest[0], rest[1])
	case "unbind":
		if len(rest) != 1 {
			return fmt.Errorf("magpie project unbind <path>")
		}
		return projectUnbind(rest[0])
	case "list":
		if len(rest) != 0 {
			return fmt.Errorf("magpie project list takes nothing else")
		}
		return projectsList()
	}
	return fmt.Errorf("%s", projectUsage)
}

// projectBind 把 path 所在项目钉在 ref 组上：绑定行 + pin 文件一次写
// 成（文件写失败行还在，重跑 bind 即修复）。不建组——错误文案教学。
func projectBind(path, ref string) error {
	g, err := findGroup(ref)
	if err != nil {
		return fmt.Errorf("%v\n  make one first: magpie group add <name> models=<m1>[,m2…] (magpie groups lists them)", err)
	}
	root := agent.ProjectRoot(path)
	if err := agent.WriteProjectPins(root, g.Ref()); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), filepath.Base(root), "pinned to", bold.Render(g.Ref()))
	fmt.Println(" ", filepath.Join(root, ".claude", "settings.local.json"))
	fmt.Println(" ", muted.Render("settings.local.json is the project's own copy and never travels with magpie; keep it out of git if the repo would take it"))
	return nil
}

// projectUnbind 先清 pin、成功后才删绑定行（次序红线：清不动的行留着
// 下次再试）；删行是整行删，快照随行走。
func projectUnbind(path string) error {
	root := agent.ProjectRoot(path)
	m, err := agent.ProjectBindings()
	if err != nil {
		return err
	}
	b, ok := m[root]
	if !ok {
		return fmt.Errorf("%s (rooted at %s) is not bound; magpie projects lists what is", path, root)
	}
	if err := agent.ClearProjectPins(root); err != nil {
		return err
	}
	delete(m, root)
	if err := agent.SaveProjectBindings(m); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), filepath.Base(root), "unpinned from", bold.Render(b.Group))
	fmt.Println(" ", muted.Render("its pins are out of settings.local.json and the binding row, snapshot with it, is gone"))
	return nil
}

// projectsList 先跑悬空对账（组没了的绑定清掉——davsync 按文件同步
// providers.json 不 fire 回调，这里就是兜底点），再列绑定与同步状态。
func projectsList() error {
	for _, root := range agent.DropGoneProjectBindings() {
		fmt.Println(amber.Render("unbound"), filepath.Base(root), muted.Render("· its group is gone"))
	}
	m, err := agent.ProjectBindings()
	if err != nil {
		return err
	}
	if len(m) == 0 {
		fmt.Println(muted.Render("no bound projects yet ·"), "magpie project bind <path> <group>", muted.Render("· pins a project's Claude Code to one routing group"))
		return nil
	}
	roots := slices.Sorted(maps.Keys(m))
	type row struct{ base, ref, sync string }
	var rows []row
	nameW, refW := 0, 0
	for _, root := range roots {
		sync := green.Render("in place")
		if agent.ProjectSync(root, m[root].Group) == agent.ProjectOutOfSync {
			sync = amber.Render("out of sync") + muted.Render(" · magpie project bind again")
		}
		r := row{base: filepath.Base(root), ref: m[root].Group, sync: sync}
		rows = append(rows, r)
		nameW = max(nameW, len(r.base))
		refW = max(refW, len(r.ref))
	}
	for _, r := range rows {
		fmt.Println(" ", bold.Render(pad(r.base, nameW)), muted.Render(pad(r.ref, refW)), r.sync)
	}
	return nil
}
