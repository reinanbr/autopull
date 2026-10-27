package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reinanbr/autopull/autopull"
)

// ─── helpers ─────────────────────────────────

func initGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := initGitRepo(t)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0644)
	gitCmd(t, dir, "add", "README.md")
	gitCmd(t, dir, "-c", "user.name=test", "-c", "user.email=t@t.com", "commit", "-m", "init")
	return dir
}

func configPathIn(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "config_auto_pull.json")
}

func writeConfigJSON(t *testing.T, path string, content map[string]any) {
	t.Helper()
	data, _ := json.Marshal(content)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// ─── CmdInit ─────────────────────────────────

func TestCmdInit_CreatesConfig(t *testing.T) {
	dir := initGitRepoWithCommit(t)
	cfgPath := configPathIn(t, dir)

	if err := autopull.CmdInit(cfgPath); err != nil {
		t.Fatalf("CmdInit failed: %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatal("config file should exist after CmdInit")
	}
}

func TestCmdInit_ConfigHasExpectedFields(t *testing.T) {
	dir := initGitRepoWithCommit(t)
	cfgPath := configPathIn(t, dir)

	if err := autopull.CmdInit(cfgPath); err != nil {
		t.Fatalf("CmdInit failed: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	var m map[string]any
	json.Unmarshal(data, &m)

	if _, ok := m["repo_path"]; !ok {
		t.Error("config should have repo_path")
	}
	if _, ok := m["branch"]; !ok {
		t.Error("config should have branch")
	}
	if _, ok := m["check_interval_seconds"]; !ok {
		t.Error("config should have check_interval_seconds")
	}
}

func TestCmdInit_NoTokenInConfig(t *testing.T) {
	dir := initGitRepoWithCommit(t)
	cfgPath := configPathIn(t, dir)

	if err := autopull.CmdInit(cfgPath); err != nil {
		t.Fatalf("CmdInit failed: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "Token") {
		t.Error("config must not contain token field")
	}
}

func TestCmdInit_FailsIfConfigAlreadyExists(t *testing.T) {
	dir := initGitRepoWithCommit(t)
	cfgPath := configPathIn(t, dir)
	os.WriteFile(cfgPath, []byte(`{"repo_path":"/tmp"}`), 0644)

	err := autopull.CmdInit(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got %v", err)
	}
}

func TestCmdInit_FailsOutsideGitRepo(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)

	// CmdInit detects the git repo from the current working directory,
	// so we must cd into a non-git dir to trigger this error path.
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	err := autopull.CmdInit(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("expected git repository error, got %v", err)
	}
}

// ─── CmdLogs ─────────────────────────────────

func TestCmdLogs_PrintsLogContent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)
	logPath := filepath.Join(dir, "auto_pull.log")

	writeConfigJSON(t, cfgPath, map[string]any{
		"repo_path": dir,
		"log_file":  "auto_pull.log",
	})
	os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0644)

	if err := autopull.CmdLogs(cfgPath, 50); err != nil {
		t.Fatalf("CmdLogs failed: %v", err)
	}
}

func TestCmdLogs_RespectsLineLimit(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)
	logPath := filepath.Join(dir, "auto_pull.log")

	writeConfigJSON(t, cfgPath, map[string]any{
		"repo_path": dir,
		"log_file":  "auto_pull.log",
	})

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "entry")
	}
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

	if err := autopull.CmdLogs(cfgPath, 5); err != nil {
		t.Fatalf("CmdLogs failed: %v", err)
	}
}

func TestCmdLogs_FailsWithBadConfig(t *testing.T) {
	if err := autopull.CmdLogs("/nonexistent/config.json", 50); err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestCmdLogs_FailsWithMissingLogFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)
	writeConfigJSON(t, cfgPath, map[string]any{
		"repo_path": dir,
		"log_file":  "auto_pull.log",
	})
	// do not create the log file
	if err := autopull.CmdLogs(cfgPath, 10); err == nil {
		t.Fatal("expected error when log file is missing")
	}
}

// ─── CmdStop ─────────────────────────────────

func TestCmdStop_NoPidFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)
	writeConfigJSON(t, cfgPath, map[string]any{"repo_path": dir})

	// no daemon running — should succeed gracefully
	if err := autopull.CmdStop(cfgPath); err != nil {
		t.Fatalf("CmdStop with no pid file should succeed: %v", err)
	}
}

func TestCmdStop_DeadPidFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)
	writeConfigJSON(t, cfgPath, map[string]any{"repo_path": dir})

	// write a pid that certainly does not exist
	pidPath := filepath.Join(dir, ".auto_pull.pid")
	os.WriteFile(pidPath, []byte("999999999"), 0644)

	if err := autopull.CmdStop(cfgPath); err != nil {
		t.Fatalf("CmdStop with dead pid should succeed: %v", err)
	}
	if _, err := os.Stat(pidPath); err == nil {
		t.Error("stale pid file should be cleaned up")
	}
}

// ─── CmdStatus ───────────────────────────────

func TestCmdStatus_NoDaemon(t *testing.T) {
	dir := initGitRepoWithCommit(t)
	cfgPath := configPathIn(t, dir)
	writeConfigJSON(t, cfgPath, map[string]any{
		"repo_path": dir,
		"branch":    "main",
	})

	// no daemon, no remote — status should not panic; fetch error is acceptable
	_ = autopull.CmdStatus(cfgPath)
}

func TestCmdStatus_FailsBadConfig(t *testing.T) {
	if err := autopull.CmdStatus("/nonexistent/config.json"); err == nil {
		t.Fatal("expected error for missing config")
	}
}

// ─── CmdDaemon ───────────────────────────────

func TestCmdDaemon_FailsWithMissingConfig(t *testing.T) {
	err := autopull.CmdDaemon("/nonexistent/config.json")
	if err == nil || !strings.Contains(err.Error(), "config file not found") {
		t.Fatalf("expected config-not-found error, got %v", err)
	}
}

// ─── CmdDryRun ───────────────────────────────

func TestCmdDryRun_FailsWithMissingConfig(t *testing.T) {
	if err := autopull.CmdDryRun("/nonexistent/config.json"); err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestCmdDryRun_FailsWithInvalidRepoPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := configPathIn(t, dir)
	writeConfigJSON(t, cfgPath, map[string]any{
		"repo_path": "/not/a/git/repo",
	})
	if err := autopull.CmdDryRun(cfgPath); err == nil {
		t.Fatal("expected error for non-git repo_path")
	}
}

// ─── CmdService ──────────────────────────────

func TestCmdService_NoArgs(t *testing.T) {
	err := autopull.CmdService([]string{})
	if runtime.GOOS == "linux" {
		if err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("expected usage error, got %v", err)
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "linux") {
			t.Fatalf("expected linux-only error, got %v", err)
		}
	}
}

func TestCmdService_UnknownAction(t *testing.T) {
	err := autopull.CmdService([]string{"bogus"})
	if runtime.GOOS == "linux" {
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("expected unknown action error, got %v", err)
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "linux") {
			t.Fatalf("expected linux-only error, got %v", err)
		}
	}
}

func TestCmdService_LogsNonNumericArgTreatedAsConfigPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("service command is linux-only")
	}
	// A non-numeric arg to "logs" now selects which repo's service to tail
	// (each repo gets its own systemd unit), not an invalid line count.
	err := autopull.CmdService([]string{"logs", "notanumber"})
	if err != nil && strings.Contains(err.Error(), "numeric") {
		t.Fatalf("non-numeric arg should be treated as a config path, not rejected: %v", err)
	}
}

func TestServiceName_DiffersPerConfigDir(t *testing.T) {
	a := autopull.ServiceName("/home/tracker.reinanbr.com/tracker_go/config_auto_pull.json")
	b := autopull.ServiceName("/home/reinanbr.com/reinanbr_nextjs/config_auto_pull.json")
	if a == b {
		t.Fatalf("expected distinct service names, both got %q", a)
	}
	if !strings.HasPrefix(a, "autopull-") || !strings.HasPrefix(b, "autopull-") {
		t.Errorf("expected autopull- prefix, got %q and %q", a, b)
	}
}

func TestServiceName_StableForSameConfig(t *testing.T) {
	cfg := "/etc/auto_pull/config_auto_pull.json"
	if got, want := autopull.ServiceName(cfg), autopull.ServiceName(cfg); got != want {
		t.Errorf("expected stable name across calls, got %q and %q", got, want)
	}
}

// ─── ServiceUser ─────────────────────────────

func TestServiceUser_FromEnvVar(t *testing.T) {
	t.Setenv("AUTOPULL_SERVICE_USER", "deploybot")
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "")
	got := autopull.ServiceUser()
	if got != "deploybot" {
		t.Errorf("got %q, want deploybot", got)
	}
}

func TestServiceUser_FallsBackToSudoUser(t *testing.T) {
	t.Setenv("AUTOPULL_SERVICE_USER", "")
	t.Setenv("SUDO_USER", "sudoer")
	t.Setenv("USER", "")
	got := autopull.ServiceUser()
	if got != "sudoer" {
		t.Errorf("got %q, want sudoer", got)
	}
}

func TestServiceUser_FallsBackToUSER(t *testing.T) {
	t.Setenv("AUTOPULL_SERVICE_USER", "")
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "alice")
	got := autopull.ServiceUser()
	if got != "alice" {
		t.Errorf("got %q, want alice", got)
	}
}

// ─── SystemdUnitContent ──────────────────────

func TestSystemdUnitContent_ContainsFields(t *testing.T) {
	content := autopull.SystemdUnitContent("/usr/local/bin/autopull", "/etc/autopull.json", "deploy")
	checks := []string{
		"[Unit]", "[Service]", "[Install]",
		"/usr/local/bin/autopull", "/etc/autopull.json",
		"User=deploy", "Restart=always",
		"RestartPreventExitStatus=78",
	}
	for _, s := range checks {
		if !strings.Contains(content, s) {
			t.Errorf("systemd unit missing %q", s)
		}
	}
}
