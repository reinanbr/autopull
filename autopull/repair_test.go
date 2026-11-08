package autopull

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoWithConfig creates a repo whose config_auto_pull.json is committed
// (when committed is non-empty) and then overwritten with local.
func repoWithConfig(t *testing.T, committed, local string) (dir, cfgPath string) {
	t.Helper()
	dir = t.TempDir()
	gitT(t, dir, "init", "-q")
	cfgPath = filepath.Join(dir, "config_auto_pull.json")
	if committed != "" {
		os.WriteFile(cfgPath, []byte(committed), 0644)
		gitT(t, dir, "add", ".")
		gitT(t, dir, "commit", "-qm", "init")
	} else {
		os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0644)
		gitT(t, dir, "add", ".")
		gitT(t, dir, "commit", "-qm", "init")
	}
	os.WriteFile(cfgPath, []byte(local), 0644)
	return dir, cfgPath
}

const committedCfg = `{
  "name": "site",
  "branch": "main",
  "post_pull_command": "( ./deploy.sh && systemctl reload nginx ) >> post_pull.log 2>&1",
  "git_recovery_mode": "hard-reset"
}
`

// What autopull v1.2.13 wrote over committedCfg: escaped, "name" dropped,
// trailing newline lost.
var damagedCfg = esc(`{
  "branch": "main",
  "post_pull_command": "( ./deploy.sh && systemctl reload nginx ) >> post_pull.log 2>&1",
  "git_recovery_mode": "hard-reset"
}`)

// esc applies Go's HTML escaping (& < >) to a config, as autopull < v1.2.14 did.
func esc(s string) string {
	b := string(rune(92))
	return strings.NewReplacer("&", b+"u0026", ">", b+"u003e", "<", b+"u003C").Replace(s)
}

func TestRepair_TrackedDamagedConfigRestoredFromHEAD(t *testing.T) {
	dir, cfgPath := repoWithConfig(t, committedCfg, damagedCfg)
	notes, err := repairConfigFile(cfgPath, dir)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != committedCfg {
		t.Errorf("not restored:\n%s", got)
	}
	if st := gitT(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("tree not clean: %q", st)
	}
}

func TestRepair_TrackedRealLocalEditIsKept(t *testing.T) {
	// A different setting (log path) is a deliberate per-server edit.
	local := strings.Replace(committedCfg, "post_pull.log", "other.log", 1)
	dir, cfgPath := repoWithConfig(t, committedCfg, local)
	notes, err := repairConfigFile(cfgPath, dir)
	if err != nil || len(notes) != 0 {
		t.Fatalf("must not touch a real edit: notes=%v err=%v", notes, err)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != local {
		t.Errorf("local edit lost:\n%s", got)
	}
}

func TestRepair_TrackedEditWithEscapesIsUnescapedNotReverted(t *testing.T) {
	local := strings.Replace(damagedCfg, "post_pull.log", "other.log", 1)
	dir, cfgPath := repoWithConfig(t, committedCfg, local)
	if _, err := repairConfigFile(cfgPath, dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(got), "( ./deploy.sh && systemctl reload nginx ) >> other.log 2>&1") {
		t.Errorf("expected unescaped local edit:\n%s", got)
	}
}

func TestRepair_TrackedLocalExtraKeyIsKept(t *testing.T) {
	local := strings.Replace(committedCfg, `"branch"`, `"note": "mine",
  "branch"`, 1)
	dir, cfgPath := repoWithConfig(t, committedCfg, local)
	if notes, _ := repairConfigFile(cfgPath, dir); len(notes) != 0 {
		t.Fatalf("local extra key must be kept: %v", notes)
	}
}

func TestRepair_UntrackedConfigUnescapedInPlace(t *testing.T) {
	dir, cfgPath := repoWithConfig(t, "", esc(`{"name": "x", "post_pull_command": "a && b >> log 2>&1 <in"}`))
	notes, err := repairConfigFile(cfgPath, dir)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	want := `{"name": "x", "post_pull_command": "a && b >> log 2>&1 <in"}` + "\n"
	if got, _ := os.ReadFile(cfgPath); string(got) != want {
		t.Errorf("got %q want %q", got, want)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil || cfg.PostPullCommand != "a && b >> log 2>&1 <in" {
		t.Errorf("reload: %v %q", err, cfg.PostPullCommand)
	}
	// Second run is a no-op.
	if notes, _ := repairConfigFile(cfgPath, dir); len(notes) != 0 {
		t.Errorf("not idempotent: %v", notes)
	}
}

func TestRepair_LiteralBackslashSequenceLeftAlone(t *testing.T) {
	// `\\u0026` is a literal backslash + "u0026", not an escaped '&'.
	local := `{"post_pull_command": "echo \\u0026"}`
	dir, cfgPath := repoWithConfig(t, "", local)
	if notes, _ := repairConfigFile(cfgPath, dir); len(notes) != 0 {
		t.Fatalf("must not rewrite: %v", notes)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != local {
		t.Errorf("changed: %s", got)
	}
}

func TestRepair_CleanAndInvalidConfigsUntouched(t *testing.T) {
	for _, local := range []string{committedCfg, `{not json`} {
		dir, cfgPath := repoWithConfig(t, "", local)
		if notes, err := repairConfigFile(cfgPath, dir); err != nil || len(notes) != 0 {
			t.Errorf("%q: notes=%v err=%v", local, notes, err)
		}
	}
}

func TestRepair_ConfigOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q")
	cfgPath := filepath.Join(t.TempDir(), "config_auto_pull.json")
	os.WriteFile(cfgPath, []byte(esc(`{"post_pull_command": "a && b"}`)), 0600)
	if notes, err := repairConfigFile(cfgPath, repo); err != nil || len(notes) != 1 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	if info, _ := os.Stat(cfgPath); info.Mode().Perm() != 0600 {
		t.Errorf("mode not preserved: %v", info.Mode())
	}
}
