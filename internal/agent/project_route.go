package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/provider"
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
// stash.json）：key 是 ProjectRoot 的规范化路径，value 是一条绑定行
// ProjectBinding——组 ref 与写前快照存在同一行里，删行即快照同生命周
// 期消失（解绑/级联删行，快照随行走，不留孤儿）。
// =====================================================================

// ProjectBinding 是一条项目绑定：组的完整 ref（group/<id>，与 usage
// 归因、pin 值同一口径）与写前 settings.local.json 原文的滚动快照。
type ProjectBinding struct {
	Group    string `json:"group"`
	Snapshot string `json:"snapshot,omitempty"`
}

// projectsPath 是绑定表文件的位置。
func projectsPath() string {
	return filepath.Join(settings.Dir(), "projects.json")
}

// ProjectBindings 读绑定表。读不出来的表是未知，不是空：空 map 会让悬
// 空对账以为没有绑定而跳过清理（failed read = unknown）。
func ProjectBindings() (map[string]ProjectBinding, error) {
	b, err := os.ReadFile(projectsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]ProjectBinding{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]ProjectBinding
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", projectsPath(), err)
	}
	if m == nil {
		m = map[string]ProjectBinding{}
	}
	return m, nil
}

// SaveProjectBindings 写整个绑定表（每行连同自己的快照）。
func SaveProjectBindings(m map[string]ProjectBinding) error {
	return writeProjects(func(old map[string]ProjectBinding) {
		clear(old)
		maps.Copy(old, m)
	})
}

// writeProjects 改 projects.json 后整体写回：文件不可解析时先 lastgood
// 旁存原文再按空表起步，绝不覆盖吞掉；写经 edit.WriteAtomic（temp+
// rename，同字节不写）。
func writeProjects(change func(map[string]ProjectBinding)) error {
	p := projectsPath()
	var m map[string]ProjectBinding
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			if kerr := lastgood.KeepUnparsed(p, lastgood.Into[map[string]ProjectBinding]); kerr != nil {
				return kerr
			}
			m = nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// 表先立起来再交给 change：新表（文件缺失/损坏重建）是 nil map，
	// 往里赋值会 panic
	if m == nil {
		m = map[string]ProjectBinding{}
	}
	change(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(p, append(out, '\n'))
}

// =====================================================================
// pin 写入器：项目 root 的 .claude/settings.local.json 里，把 Claude
// Code 选模型的每一个 env 出口都钉在绑定组上（fork 的「绑定即唯一出
// 口」语义——部分 pin 留逃逸口 = 功能打折）。整组键一起写也一起清：
// 残留半组 pin（tier 行为不一致）比不 pin 更糟。
// =====================================================================

// projectPinEnv 是被钉住的模型键全集：四个 tier 默认、主模型、旧版
// Claude Code 仍读的 SMALL_FAST、子代理与 auto mode 分类器（claude.go
// 的 claudeEnv/claudeAutoModeEnv 同一清单）。窗口键 claudeContextEnv
// 配套写入但单独取值。
var projectPinEnv = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL", claudeAutoModeEnv,
}

// localSettingsPath 是项目根下的 settings.local.json（个人私有配置，
// 天然不进 git）。
func localSettingsPath(root string) string {
	return filepath.Join(root, ".claude", "settings.local.json")
}

// WriteProjectPins 把 root 的 settings.local.json 的 env 钉在 ref 上：
// 模型键全 = ref，窗口键 = 组的最小成员窗口。写前把原文滚动快照进该
// root 绑定行的 Snapshot（每次写前、单份、rebind 覆盖；文件缺失或空白
// 不快照、行内旧值保留）。绑定表先行：行落表（没有先建）后文件才写，
// 文件写失败行还在，重跑 bind 即修复（fork write_failure_keeps_db_row）。
func WriteProjectPins(root, ref string) error {
	p := localSettingsPath(root)
	orig, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := writeProjects(func(m map[string]ProjectBinding) {
		b := m[root] // 行不存在时零值：Group 待设、Snapshot 待填
		b.Group = ref
		if len(bytes.TrimSpace(orig)) > 0 {
			b.Snapshot = string(orig)
		}
		m[root] = b
	}); err != nil {
		return err
	}
	kvs := make([]edit.KV, 0, len(projectPinEnv)+1)
	for _, k := range projectPinEnv {
		kvs = append(kvs, edit.KV{Path: "env." + k, Value: ref})
	}
	kvs = append(kvs, edit.KV{Path: "env." + claudeContextEnv, Value: strconv.Itoa(projectWindow(ref))})
	if err := edit.SetJSON(p, kvs...); err != nil {
		if !validLocalJSON(orig) {
			// 损坏文件：原文已快照，按最小结构重建（fork
			// set_rebuilds_corrupted_json 同形）
			return rebuildLocalSettings(p, kvs)
		}
		return err
	}
	return nil
}

// projectWindow 是 ref 组告诉 Claude Code 的上下文窗口：共享模型清单
// 里该组的 Context（成员最小窗口），找不到或为 0 用 magpie 的默认工作
// 窗口（WorkingWindow 常量，非 claude- 开头 ID 被按 200K 假设的坑）。
func projectWindow(ref string) int {
	for _, m := range sharedModels("claude") {
		if m.ID == ref && m.Context > 0 {
			return m.Context
		}
	}
	return settings.WorkingWindow
}

// validLocalJSON 说 b 是否是 SetJSON 能编辑的 JSON/JSONC（空与缺失不
// 算损坏）。判定与 edit.SetJSON 的解析同一口径（注释剥离后 json.Valid），
// 带注释的合法文件走 SetJSON 的保格式路径，不会被误重建。
func validLocalJSON(b []byte) bool {
	if len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	return json.Valid(jsonc.ToJSON(edit.TrimBOM(b)))
}

// rebuildLocalSettings 把损坏的 settings.local.json 重建为只含 pin 的
// 最小结构（原文已在该行的 Snapshot 里，可手工取回）。
func rebuildLocalSettings(p string, kvs []edit.KV) error {
	env := map[string]string{}
	for _, kv := range kvs {
		env[strings.TrimPrefix(kv.Path, "env.")] = fmt.Sprint(kv.Value)
	}
	b, err := json.MarshalIndent(map[string]any{"env": env}, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(p, append(b, '\n'))
}

// =====================================================================
// pin 清除与同步状态。
// =====================================================================

// ClearProjectPins 拔掉 root 的全部 pin 键（模型 8 键 + 窗口键，整组一
// 起清——残留半组 pin 让 tier 行为不一致，比不 pin 更糟）。文件缺失或
// 本来一个 pin 键都没有时不动用户文件（幂等）；确有 pin 要拔时写前同
// 样滚动快照进该行的 Snapshot（fork remove 写前快照同理）——行不存在
// 则不建行（快照只随行存在，不落孤儿）。
func ClearProjectPins(root string) error {
	p := localSettingsPath(root)
	has := false
	for _, k := range projectPinEnv {
		if _, ok := edit.GetJSON(p, "env."+k); ok {
			has = true
			break
		}
	}
	if !has {
		return nil
	}
	if orig, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(orig)) > 0 {
		if err := writeProjects(func(m map[string]ProjectBinding) {
			if b, ok := m[root]; ok {
				b.Snapshot = string(orig)
				m[root] = b
			}
		}); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(projectPinEnv)+1)
	for _, k := range projectPinEnv {
		paths = append(paths, "env."+k)
	}
	paths = append(paths, "env."+claudeContextEnv)
	return edit.DelJSON(p, paths...)
}

// ProjectSyncState 是一个绑定项目的文件 pin 与绑定表的一致状态。
type ProjectSyncState int

const (
	// ProjectSynced：全部模型 pin 键都在且等于绑定的 ref。
	ProjectSynced ProjectSyncState = iota
	// ProjectOutOfSync：有键缺失或值不同（文件缺失也算）——重跑 bind
	// 即修复。
	ProjectOutOfSync
)

// ProjectSync 说 root 的文件 pin 是否还钉在 ref 上。只看模型键：窗口
// 键的值随组配置变化，不是漂移信号。
func ProjectSync(root, ref string) ProjectSyncState {
	p := localSettingsPath(root)
	for _, k := range projectPinEnv {
		if v, ok := edit.GetJSON(p, "env."+k); !ok || v != ref {
			return ProjectOutOfSync
		}
	}
	return ProjectSynced
}

// =====================================================================
// 悬空级联：组删了/hidden/disabled，绑它的项目的 pin 指向死组——Claude
// Code 会持续发 group/<dead-id> 全部失败，必须清。挂点 = provider 的组
// 写入单点（store 成功后 fire GroupsChanged，覆盖 CLI/GUI/TUI 全部入口；
// fork spec §4.1 的「挂 DAO 一处覆盖全部入口」同理）。davsync 按文件同
// 步 providers.json 不 fire——兜底是 magpie projects 的 list 对账。
// =====================================================================

// init 把悬空清理挂到组变化订阅点上。
func init() {
	provider.GroupsChanged = append(provider.GroupsChanged, func() { DropGoneProjectBindings() })
}

// DropGoneProjectBindings 清理悬空绑定：组不在共享模型清单（hidden/
// disabled/删除/auto-off 都不在）的绑定，先清 pin、成功后才删绑定行
// （整条 ProjectBinding 删，快照随行走）。次序红线：清 pin 失败（权限/
// IO）→ 留行（快照随行留）、记日志、待下次 fire 或 magpie projects 对
// 账重试——先删行 + 清 pin 失败 = 死 pin 永久残留且按行驱动的对账看
// 不见。返回的 dropped 只含清理成功者。
func DropGoneProjectBindings() (dropped []string) {
	m, err := ProjectBindings()
	if err != nil {
		log.Printf("project bindings can't be read: %v (nothing dropped)", err)
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	models := sharedModels("claude")
	served := func(ref string) bool {
		for _, mo := range models {
			if mo.ID == ref {
				return true
			}
		}
		return false
	}
	changed := false
	keep := make(map[string]ProjectBinding, len(m))
	for root, b := range m {
		if served(b.Group) {
			keep[root] = b
			continue
		}
		if err := ClearProjectPins(root); err != nil {
			log.Printf("clearing pins of %s: %v (kept for the next try)", root, err)
			keep[root] = b
			continue
		}
		changed = true
		dropped = append(dropped, root)
	}
	if !changed {
		return nil
	}
	if err := SaveProjectBindings(keep); err != nil {
		log.Printf("saving project bindings: %v", err)
	}
	return dropped
}
