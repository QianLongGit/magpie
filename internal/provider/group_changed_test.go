package provider

import (
	"testing"
)

// =====================================================================
// GroupsChanged：组集合变化的订阅点（fire 在 providers.json 的写入单点
// store 成功后——对齐 fork spec §4.1「挂到 DAO 一处即覆盖全部入口」的
// 原则；SaveGroup/DeleteGroup/SwitchGroup 及一切现行与未来的组变更路径
// 都过这里）。订阅者：internal/agent 的项目绑定级联。
// =====================================================================

// changedCounter 订阅 GroupsChanged，返回当前计数；清理时摘掉自己。
func changedCounter(t *testing.T) *int {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fired := new(int)
	was := len(GroupsChanged)
	GroupsChanged = append(GroupsChanged, func() { *fired++ })
	t.Cleanup(func() { GroupsChanged = GroupsChanged[:was] })
	return fired
}

// sandboxGroup 造一个有一名真成员的组（成员挂在哑 chat 端点的 provider
// 上，never dialed——decide_group_test 的造法）。
func sandboxGroup(t *testing.T, id string) {
	t.Helper()
	if err := Save(Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: id, Name: id, Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
}

func TestSavingAGroupFiresGroupsChanged(t *testing.T) {
	fired := changedCounter(t)
	sandboxGroup(t, "proj-x")
	before := *fired
	if err := SaveGroup(Group{ID: "proj-x", Name: "renamed", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if *fired <= before {
		t.Fatalf("saving a group fired %d times, want more than %d", *fired-before, 0)
	}
}

func TestDeletingGroupsFiresGroupsChangedToo(t *testing.T) {
	fired := changedCounter(t)
	sandboxGroup(t, "proj-y")
	before := *fired
	if err := DeleteGroup("proj-y"); err != nil {
		t.Fatal(err)
	}
	if *fired <= before {
		t.Fatalf("deleting a group fired %d times, want more than %d", *fired-before, 0)
	}
	sandboxGroup(t, "proj-z")
	before = *fired
	if err := DeleteGroups([]string{"proj-z"}); err != nil {
		t.Fatal(err)
	}
	if *fired <= before {
		t.Fatalf("deleting groups fired %d times, want more than %d", *fired-before, 0)
	}
}

// SwitchGroup 直接 store 不过 SaveGroup——GUI 组卡片开关让组 disabled，
// 项目绑定按悬空处理，必须同样 fire（LESSONS：a fix reaches every
// sibling）。
func TestSwitchingAGroupOffFiresGroupsChanged(t *testing.T) {
	fired := changedCounter(t)
	sandboxGroup(t, "proj-w")
	before := *fired
	if err := SwitchGroup("proj-w", false); err != nil {
		t.Fatal(err)
	}
	if *fired <= before {
		t.Fatalf("switching a group off fired %d times, want more than %d", *fired-before, 0)
	}
}
