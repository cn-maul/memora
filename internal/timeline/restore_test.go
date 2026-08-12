package timeline

import (
	"errors"
	"testing"

	"memora/internal/contract"
)

// fakeGit 记录调用，验证 Restore 的自动快照行为
type fakeGit struct {
	status      map[string]string
	restoreTo   string
	commitCalls int
	commitAuto  func() (string, bool, error)
}

func (f *fakeGit) Log() ([]*contract.CommitInfo, error) { return nil, nil }
func (f *fakeGit) DiffStats(hash string) (*contract.DiffStat, error) {
	return nil, nil
}
func (f *fakeGit) FileHistory(relPath string) ([]*contract.CommitInfo, error) {
	return nil, nil
}
func (f *fakeGit) ShowFileAt(relPath, hash string) (string, error) { return "", nil }
func (f *fakeGit) RestoreFile(relPath, hash string) error {
	f.restoreTo = relPath + "@" + hash
	return nil
}
func (f *fakeGit) Status() (map[string]string, error) { return f.status, nil }
func (f *fakeGit) DiffContents() (string, error)      { return "", nil }
func (f *fakeGit) CommitAuto(files []string) (string, bool, error) {
	f.commitCalls++
	if f.commitAuto != nil {
		return f.commitAuto()
	}
	return "h", false, nil
}

type fakeEvents struct{ notified []string }

func (f *fakeEvents) Notify(topic string, data interface{}) { f.notified = append(f.notified, topic) }

func newTestModule(g *fakeGit, ev *fakeEvents) *Module {
	return New(g, nil, nil, ev, "")
}

// 工作区有未保存改动时：Restore 应先自动提交（自动快照）再恢复，不再返回 409
func TestRestoreAutoSnapshotWhenDirty(t *testing.T) {
	g := &fakeGit{status: map[string]string{"a.txt": "M", "b.txt": "?"}}
	ev := &fakeEvents{}
	m := newTestModule(g, ev)

	if err := m.Restore("a.txt", "abcd"); err != nil {
		t.Fatalf("脏工作区也应能恢复: %v", err)
	}
	if g.commitCalls != 1 {
		t.Fatalf("应触发 1 次自动快照提交，got %d", g.commitCalls)
	}
	if g.restoreTo != "a.txt@abcd" {
		t.Fatalf("应执行恢复，got %q", g.restoreTo)
	}
	// 快照提交应广播 commit_done 让前端刷新
	found := false
	for _, n := range ev.notified {
		if n == "commit_done" {
			found = true
		}
	}
	if !found {
		t.Fatalf("自动快照后应广播 commit_done，got %v", ev.notified)
	}
}

// 工作区干净时：直接恢复，不产生多余提交
func TestRestoreCleanWorkspaceNoSnapshot(t *testing.T) {
	g := &fakeGit{status: map[string]string{"b.txt": "?"}} // 未跟踪文件不算脏
	ev := &fakeEvents{}
	m := newTestModule(g, ev)

	if err := m.Restore("a.txt", "abcd"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if g.commitCalls != 0 {
		t.Fatalf("干净工作区不应触发提交，got %d", g.commitCalls)
	}
	if g.restoreTo != "a.txt@abcd" {
		t.Fatalf("应执行恢复，got %q", g.restoreTo)
	}
}

// 自动快照本身失败时应报错，不执行恢复（避免丢失当前改动）
func TestRestoreSnapshotFailureAborts(t *testing.T) {
	g := &fakeGit{
		status:     map[string]string{"a.txt": "M"},
		commitAuto: func() (string, bool, error) { return "", false, errors.New("boom") },
	}
	m := newTestModule(g, &fakeEvents{})

	if err := m.Restore("a.txt", "abcd"); err == nil {
		t.Fatal("自动快照失败时恢复应报错")
	}
	if g.restoreTo != "" {
		t.Fatalf("快照失败不应执行恢复，got %q", g.restoreTo)
	}
}
