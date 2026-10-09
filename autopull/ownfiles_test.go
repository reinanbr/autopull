package autopull

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupClone returns (upstream work dir, server clone) sharing a bare remote,
// with config_auto_pull.json committed.
func setupClone(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	dev := filepath.Join(root, "dev")
	git(t, root, "clone", "-q", "remote.git", dev)
	os.WriteFile(filepath.Join(dev, "app.txt"), []byte("v1\n"), 0644)
	os.WriteFile(filepath.Join(dev, "config_auto_pull.json"), []byte("{}\n"), 0644)
	git(t, dev, "add", ".")
	git(t, dev, "commit", "-qm", "init")
	git(t, dev, "push", "-q", "origin", "main")
	srv := filepath.Join(root, "srv")
	git(t, root, "clone", "-q", "remote.git", srv)
	return dev, srv
}

func syncOnce(t *testing.T, srv string) {
	t.Helper()
	cfgPath := filepath.Join(srv, "config_auto_pull.json")
	cfg := &Config{RepoPath: srv, Branch: "main", LogFile: "auto_pull.log"}
	if _, err := remoteCommit(srv, "main", ""); err != nil {
		t.Fatal(err)
	}
	if changes := filterOwnChanges(mustChanges(t, srv), ownFiles(t, cfg, cfgPath)); len(changes) != 0 {
		t.Fatalf("own config must not count as dirty, got %v", changes)
	}
	g := guardOwnFiles(cfg, cfgPath, "origin/main")
	out, err := mergeFastForward(srv, "main")
	g.restore()
	if err != nil {
		t.Fatalf("merge failed: %v\n%s", err, out)
	}
}

func mustChanges(t *testing.T, dir string) []string {
	c, err := TrackedChanges(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ownFiles(t *testing.T, cfg *Config, cfgPath string) []ownFile {
	_, f := ownFilesInRepo(cfg, cfgPath)
	return f
}

// Regression: a committed config edited locally on the server used to block
// every pull touching it ("local changes would be overwritten by merge").
func TestGuard_LocalEditedCommittedConfigSurvivesUpstreamEdit(t *testing.T) {
	dev, srv := setupClone(t)
	local := []byte(`{"git_recovery_mode":"stash"}` + "\n")
	os.WriteFile(filepath.Join(srv, "config_auto_pull.json"), local, 0644)

	os.WriteFile(filepath.Join(dev, "app.txt"), []byte("v2\n"), 0644)
	os.WriteFile(filepath.Join(dev, "config_auto_pull.json"), []byte(`{"branch":"main"}`+"\n"), 0644)
	git(t, dev, "commit", "-qam", "upstream")
	git(t, dev, "push", "-q", "origin", "main")

	syncOnce(t, srv)

	if b, _ := os.ReadFile(filepath.Join(srv, "app.txt")); string(b) != "v2\n" {
		t.Errorf("update not applied: app.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(srv, "config_auto_pull.json")); string(b) != string(local) {
		t.Errorf("local config not preserved: %q", b)
	}
}

// Regression: following the "git rm --cached" hint upstream deleted the
// config on every server that pulled it.
func TestGuard_ConfigRestoredWhenUpstreamUntracksIt(t *testing.T) {
	dev, srv := setupClone(t)
	git(t, dev, "rm", "-q", "--cached", "config_auto_pull.json")
	git(t, dev, "commit", "-qm", "untrack")
	git(t, dev, "push", "-q", "origin", "main")

	syncOnce(t, srv)

	if b, err := os.ReadFile(filepath.Join(srv, "config_auto_pull.json")); err != nil || string(b) != "{}\n" {
		t.Errorf("config should be restored after upstream untracked it: %q %v", b, err)
	}
}

// An unmodified tracked config follows upstream, like any other file.
func TestGuard_UnmodifiedConfigTakesUpstreamVersion(t *testing.T) {
	dev, srv := setupClone(t)
	os.WriteFile(filepath.Join(dev, "config_auto_pull.json"), []byte(`{"branch":"main"}`+"\n"), 0644)
	git(t, dev, "commit", "-qam", "upstream cfg")
	git(t, dev, "push", "-q", "origin", "main")

	syncOnce(t, srv)

	if b, _ := os.ReadFile(filepath.Join(srv, "config_auto_pull.json")); string(b) != `{"branch":"main"}`+"\n" {
		t.Errorf("expected upstream config, got %q", b)
	}
}

func TestStatusPath(t *testing.T) {
	cases := map[string]string{
		"M config_auto_pull.json": "config_auto_pull.json",
		"MM a/b.txt":              "a/b.txt",
		"R old.txt -> new.txt":    "new.txt",
		`M "sp ace.txt"`:          "sp ace.txt",
	}
	for in, want := range cases {
		if got := statusPath(in); got != want {
			t.Errorf("statusPath(%q) = %q, want %q", in, got, want)
		}
	}
}
