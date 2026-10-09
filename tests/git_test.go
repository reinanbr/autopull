package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reinanbr/autopull/autopull"
)

// ─── ShortHash ───────────────────────────────

func TestShortHash(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc1234def567", "abc1234"},
		{"abc1234", "abc1234"},
		{"abc12", "abc12"},
		{"", ""},
		{"1234567890", "1234567"},
	}
	for _, c := range cases {
		got := autopull.ShortHash(c.in)
		if got != c.want {
			t.Errorf("ShortHash(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── ParseAheadBehind ────────────────────────

func TestParseAheadBehind_Valid(t *testing.T) {
	ahead, behind, err := autopull.ParseAheadBehind("2\t3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ahead != 2 || behind != 3 {
		t.Fatalf("got ahead=%d behind=%d, want 2 3", ahead, behind)
	}
}

func TestParseAheadBehind_Zero(t *testing.T) {
	ahead, behind, err := autopull.ParseAheadBehind("0\t0")
	if err != nil || ahead != 0 || behind != 0 {
		t.Fatalf("got %d %d %v, want 0 0 nil", ahead, behind, err)
	}
}

func TestParseAheadBehind_InvalidFormat(t *testing.T) {
	if _, _, err := autopull.ParseAheadBehind("bad output"); err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestParseAheadBehind_SingleField(t *testing.T) {
	if _, _, err := autopull.ParseAheadBehind("5"); err == nil {
		t.Fatal("expected error for single field")
	}
}

// ─── GitRecoveryHints ────────────────────────

func TestGitRecoveryHints_DirtyOnly(t *testing.T) {
	hints := autopull.GitRecoveryHints(0, 0, true)
	if len(hints) == 0 {
		t.Fatal("expected hints for dirty tree")
	}
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "dirty") && !strings.Contains(joined, "Working tree") {
		t.Error("hints should mention dirty working tree")
	}
	// Regression: `git add -A` stages untracked files too, which is how
	// auto_pull's own config/log/state files have ended up permanently
	// tracked in watched repos. `git add -u` only stages already-tracked
	// changes, matching what TrackedChanges actually detects.
	if strings.Contains(joined, "git add -A") {
		t.Error("dirty-tree hint must not suggest `git add -A` — use `git add -u` instead")
	}
	if !strings.Contains(joined, "git add -u") {
		t.Error("dirty-tree hint should suggest `git add -u`")
	}
}

func TestGitRecoveryHints_AheadOnly(t *testing.T) {
	hints := autopull.GitRecoveryHints(3, 0, false)
	if len(hints) == 0 {
		t.Fatal("expected hints when ahead")
	}
	if !strings.Contains(strings.Join(hints, "\n"), "ahead") {
		t.Error("hints should mention ahead")
	}
}

func TestGitRecoveryHints_BehindOnly(t *testing.T) {
	hints := autopull.GitRecoveryHints(0, 2, false)
	if len(hints) == 0 {
		t.Fatal("expected hints when behind")
	}
	if !strings.Contains(strings.Join(hints, "\n"), "behind") {
		t.Error("hints should mention behind")
	}
}

func TestGitRecoveryHints_Diverged(t *testing.T) {
	hints := autopull.GitRecoveryHints(1, 2, false)
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "diverged") {
		t.Error("hints should mention diverged")
	}
}

func TestGitRecoveryHints_Clean(t *testing.T) {
	hints := autopull.GitRecoveryHints(0, 0, false)
	if len(hints) != 0 {
		t.Errorf("expected no hints for clean in-sync repo, got %v", hints)
	}
}

// ─── GitPullErrorHints ───────────────────────

func TestGitPullErrorHints_AuthFailed(t *testing.T) {
	hints := autopull.GitPullErrorHints("authentication failed: bad credentials")
	if len(hints) == 0 {
		t.Fatal("expected hints for auth failure")
	}
	if !strings.Contains(strings.Join(hints, "\n"), "AUTOPULL_TOKEN") {
		t.Error("hints should mention token")
	}
}

func TestGitPullErrorHints_RepoNotFound(t *testing.T) {
	hints := autopull.GitPullErrorHints("repository not found")
	if len(hints) == 0 {
		t.Fatal("expected hints for repo not found")
	}
}

func TestGitPullErrorHints_BranchNotFound(t *testing.T) {
	hints := autopull.GitPullErrorHints("couldn't find remote ref main")
	if len(hints) == 0 {
		t.Fatal("expected hints for branch not found")
	}
	if !strings.Contains(strings.Join(hints, "\n"), "git branch") {
		t.Error("hints should suggest git branch -r")
	}
}

func TestGitPullErrorHints_Conflict(t *testing.T) {
	hints := autopull.GitPullErrorHints("merge conflict detected")
	if len(hints) == 0 {
		t.Fatal("expected hints for conflict")
	}
}

func TestGitPullErrorHints_LocalChangesBlocking(t *testing.T) {
	hints := autopull.GitPullErrorHints("would be overwritten by merge")
	if len(hints) == 0 {
		t.Fatal("expected hints for local changes blocking pull")
	}
}

func TestGitPullErrorHints_UnknownError(t *testing.T) {
	hints := autopull.GitPullErrorHints("some random network error")
	if len(hints) != 0 {
		t.Errorf("expected no hints for unknown error, got %v", hints)
	}
}

func TestGitPullErrorHints_UntrackedCollision(t *testing.T) {
	msg := "error: The following untracked working tree files would be overwritten by merge:\n\tconfig_auto_pull.json"
	hints := autopull.GitPullErrorHints(msg)
	if len(hints) == 0 || !strings.Contains(hints[0], "Untracked") {
		t.Errorf("expected untracked-collision hint, got %v", hints)
	}
}

func TestGitPullErrorHints_NotFastForward(t *testing.T) {
	hints := autopull.GitPullErrorHints("fatal: Not possible to fast-forward, aborting.")
	if len(hints) == 0 || !strings.Contains(hints[0], "diverged") {
		t.Errorf("expected divergence hint, got %v", hints)
	}
}

// ─── ArtifactGitignoreHints ──────────────────

func TestArtifactGitignoreHints_NodeModules(t *testing.T) {
	cmds := autopull.ArtifactGitignoreHints([]string{"?? node_modules/"})
	if len(cmds) == 0 {
		t.Fatal("expected gitignore hint for node_modules")
	}
	if !strings.Contains(cmds[0], "node_modules") {
		t.Errorf("hint should reference node_modules, got: %s", cmds[0])
	}
}

func TestArtifactGitignoreHints_DistDir(t *testing.T) {
	cmds := autopull.ArtifactGitignoreHints([]string{"?? dist/bundle.js"})
	if len(cmds) == 0 {
		t.Fatal("expected gitignore hint for dist/")
	}
}

func TestArtifactGitignoreHints_DBWal(t *testing.T) {
	cmds := autopull.ArtifactGitignoreHints([]string{"M  app.db-wal"})
	if len(cmds) == 0 {
		t.Fatal("expected gitignore hint for .db-wal file")
	}
	if !strings.Contains(cmds[0], ".db-wal") {
		t.Errorf("hint should reference .db-wal, got: %s", cmds[0])
	}
}

func TestArtifactGitignoreHints_RuntimePidFile(t *testing.T) {
	cmds := autopull.ArtifactGitignoreHints([]string{"M  .auto_pull.pid"})
	if len(cmds) == 0 {
		t.Fatal("expected gitignore hint for .auto_pull.pid")
	}
}

func TestArtifactGitignoreHints_NoDuplicates(t *testing.T) {
	lines := []string{"?? node_modules/foo", "?? node_modules/bar"}
	cmds := autopull.ArtifactGitignoreHints(lines)
	if len(cmds) != 1 {
		t.Errorf("expected 1 hint (deduped), got %d", len(cmds))
	}
}

func TestArtifactGitignoreHints_NoKnownArtifacts(t *testing.T) {
	cmds := autopull.ArtifactGitignoreHints([]string{"M  src/main.go"})
	if len(cmds) != 0 {
		t.Errorf("expected no hints for regular source file, got %v", cmds)
	}
}

func TestArtifactGitignoreHints_Empty(t *testing.T) {
	if cmds := autopull.ArtifactGitignoreHints(nil); len(cmds) != 0 {
		t.Errorf("expected no hints for nil input, got %v", cmds)
	}
}

// ─── IsRepoDirty ─────────────────────────────

func TestIsRepoDirty_IgnoresUntrackedFiles(t *testing.T) {
	dir := initGitRepo(t)
	os.WriteFile(filepath.Join(dir, "scratch.tmp"), []byte("x"), 0644)
	if autopull.IsRepoDirty(dir) {
		t.Fatal("untracked file should not make repo dirty")
	}
}

func TestIsRepoDirty_DetectsModifiedTrackedFile(t *testing.T) {
	dir := initGitRepo(t)
	tracked := filepath.Join(dir, "file.txt")
	os.WriteFile(tracked, []byte("v1\n"), 0644)
	gitCmd(t, dir, "add", "file.txt")
	gitCmd(t, dir, "-c", "user.name=test", "-c", "user.email=t@t.com", "commit", "-m", "init")
	os.WriteFile(tracked, []byte("v2\n"), 0644)
	if !autopull.IsRepoDirty(dir) {
		t.Fatal("modified tracked file should make repo dirty")
	}
}

func TestIsRepoDirty_CleanRepo(t *testing.T) {
	dir := initGitRepo(t)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0644)
	gitCmd(t, dir, "add", "f.txt")
	gitCmd(t, dir, "-c", "user.name=test", "-c", "user.email=t@t.com", "commit", "-m", "init")
	if autopull.IsRepoDirty(dir) {
		t.Fatal("committed repo should not be dirty")
	}
}

// ─── EnsureGitRepo ───────────────────────────

func TestEnsureGitRepo_ValidRepo(t *testing.T) {
	dir := initGitRepo(t)
	if err := autopull.EnsureGitRepo(dir); err != nil {
		t.Fatalf("expected no error for valid git repo: %v", err)
	}
}

func TestEnsureGitRepo_NotARepo(t *testing.T) {
	dir := t.TempDir()
	if err := autopull.EnsureGitRepo(dir); err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestEnsureGitRepo_EmptyPath(t *testing.T) {
	if err := autopull.EnsureGitRepo(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestEnsureGitRepo_NonExistentPath(t *testing.T) {
	if err := autopull.EnsureGitRepo("/nonexistent/path/xyz"); err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

// ─── TrackedChanges ──────────────────────────

func TestTrackedChanges_CleanRepo(t *testing.T) {
	dir := initGitRepo(t)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0644)
	gitCmd(t, dir, "add", "f.txt")
	gitCmd(t, dir, "-c", "user.name=test", "-c", "user.email=t@t.com", "commit", "-m", "init")
	changes, err := autopull.TrackedChanges(dir)
	if err != nil || len(changes) != 0 {
		t.Fatalf("expected no changes, got %v %v", changes, err)
	}
}

func TestTrackedChanges_ModifiedFile(t *testing.T) {
	dir := initGitRepo(t)
	tracked := filepath.Join(dir, "f.txt")
	os.WriteFile(tracked, []byte("v1\n"), 0644)
	gitCmd(t, dir, "add", "f.txt")
	gitCmd(t, dir, "-c", "user.name=test", "-c", "user.email=t@t.com", "commit", "-m", "init")
	os.WriteFile(tracked, []byte("v2\n"), 0644)
	changes, err := autopull.TrackedChanges(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("expected changes for modified tracked file")
	}
}

func TestTrackedChanges_IgnoresUntracked(t *testing.T) {
	dir := initGitRepo(t)
	os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0644)
	changes, err := autopull.TrackedChanges(dir)
	if err != nil || len(changes) != 0 {
		t.Fatalf("expected no changes for untracked-only, got %v %v", changes, err)
	}
}

// ─── OwnFileTrackingHints ────────────────────
//
// Regression coverage for: a repo where config_auto_pull.json got committed
// (e.g. by following the old "git add -A" recovery hint) later fails plain
// `git pull` with "local changes would be overwritten by merge" — even
// though `autopull status` reports the tree as clean, because
// --assume-unchanged hides the file from `git status`/TrackedChanges.
// OwnFileTrackingHints must catch this by checking the index directly.

func TestOwnFileTrackingHints_DetectsTrackedConfigDespiteAssumeUnchanged(t *testing.T) {
	dir := initGitRepo(t)
	cfgPath := filepath.Join(dir, "config_auto_pull.json")
	os.WriteFile(cfgPath, []byte(`{"repo_path":"."}`), 0644)
	gitCmd(t, dir, "add", "config_auto_pull.json")
	gitCmd(t, dir, "-c", "user.name=test", "-c", "user.email=t@t.com", "commit", "-m", "oops, committed config")

	// Simulate what autopull <= v1.2.11 did once the file was tracked.
	gitCmd(t, dir, "update-index", "--assume-unchanged", "config_auto_pull.json")
	os.WriteFile(cfgPath, []byte(`{"repo_path":".","check_interval_seconds":5}`), 0644)

	if changes, _ := autopull.TrackedChanges(dir); len(changes) != 0 {
		t.Fatalf("assume-unchanged should hide the file from TrackedChanges, got %v", changes)
	}

	cfg := &autopull.Config{RepoPath: dir, LogFile: "auto_pull.log"}
	hints := autopull.OwnFileTrackingHints(cfg, cfgPath)
	if len(hints) == 0 {
		t.Fatal("expected a hint for the tracked config file even though git status is silent")
	}
	if !strings.Contains(hints[0], "config_auto_pull.json") || !strings.Contains(hints[0], "git rm --cached") {
		t.Errorf("hint should name the file and how to untrack it, got: %s", hints[0])
	}
}

func TestOwnFileTrackingHints_NoneWhenNeverTracked(t *testing.T) {
	dir := initGitRepo(t)
	cfgPath := filepath.Join(dir, "config_auto_pull.json")
	os.WriteFile(cfgPath, []byte(`{"repo_path":"."}`), 0644)

	cfg := &autopull.Config{RepoPath: dir, LogFile: "auto_pull.log"}
	hints := autopull.OwnFileTrackingHints(cfg, cfgPath)
	if len(hints) != 0 {
		t.Errorf("expected no hints for an untracked config file, got %v", hints)
	}
}

// ─── git helpers ─────────────────────────────

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	return dir
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
