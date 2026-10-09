package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reinanbr/autopull/autopull"
)

// ─── ResolveConfigPath ───────────────────────

func TestResolveConfigPath_EmptyUsesDefault(t *testing.T) {
	got := autopull.ResolveConfigPath("")
	if !strings.HasSuffix(got, "config_auto_pull.json") {
		t.Errorf("got %q, want suffix config_auto_pull.json", got)
	}
}

func TestResolveConfigPath_CustomName(t *testing.T) {
	got := autopull.ResolveConfigPath("myconfig.json")
	if !strings.HasSuffix(got, "myconfig.json") {
		t.Errorf("got %q, want suffix myconfig.json", got)
	}
}

func TestResolveConfigPath_ReturnsAbsolute(t *testing.T) {
	got := autopull.ResolveConfigPath("relative.json")
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %q", got)
	}
}

// ─── NormalizeRecoveryMode ───────────────────

func TestNormalizeRecoveryMode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "off"},
		{"off", "off"},
		{"OFF", "off"},
		{"stash", "stash"},
		{"STASH", "stash"},
		{"hard-reset", "hard-reset"},
		{" HARD-RESET ", "hard-reset"},
		{"hard_reset", "hard-reset"},
		{"invalid", "off"},
		{"reset", "off"},
	}
	for _, c := range cases {
		got := autopull.NormalizeRecoveryMode(c.in)
		if got != c.want {
			t.Errorf("NormalizeRecoveryMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── LoadDotEnvToken ─────────────────────────

func TestLoadDotEnvToken_AUTOPULL_TOKEN(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "AUTOPULL_TOKEN=mytoken\n")
	got := autopull.LoadDotEnvToken(dir)
	if got != "mytoken" {
		t.Errorf("got %q, want %q", got, "mytoken")
	}
}

func TestLoadDotEnvToken_GITHUB_TOKEN(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "GITHUB_TOKEN=ghp_xyz\n")
	got := autopull.LoadDotEnvToken(dir)
	if got != "ghp_xyz" {
		t.Errorf("got %q, want %q", got, "ghp_xyz")
	}
}

func TestLoadDotEnvToken_AUTOPULL_TakesPriority(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "AUTOPULL_TOKEN=first\nGITHUB_TOKEN=second\n")
	got := autopull.LoadDotEnvToken(dir)
	if got != "first" {
		t.Errorf("got %q, want first", got)
	}
}

func TestLoadDotEnvToken_QuotedValue(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, `AUTOPULL_TOKEN="quoted"`+"\n")
	got := autopull.LoadDotEnvToken(dir)
	if got != "quoted" {
		t.Errorf("got %q, want %q", got, "quoted")
	}
}

func TestLoadDotEnvToken_CommentsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "# AUTOPULL_TOKEN=nope\nGITHUB_TOKEN=real\n")
	got := autopull.LoadDotEnvToken(dir)
	if got != "real" {
		t.Errorf("got %q, want %q", got, "real")
	}
}

func TestLoadDotEnvToken_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "")
	if got := autopull.LoadDotEnvToken(dir); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestLoadDotEnvToken_UnrelatedKeys(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "FOO=bar\nBAZ=qux\n")
	if got := autopull.LoadDotEnvToken(dir); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestLoadDotEnvToken_MissingFile(t *testing.T) {
	if got := autopull.LoadDotEnvToken(t.TempDir()); got != "" {
		t.Errorf("expected empty for missing .env, got %q", got)
	}
}

// ─── LoadConfig ──────────────────────────────

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{"repo_path": dir})
	cfg, err := autopull.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Branch != "main" {
		t.Errorf("Branch = %q, want main", cfg.Branch)
	}
	if cfg.CheckIntervalSeconds != 5 {
		t.Errorf("CheckIntervalSeconds = %d, want 5", cfg.CheckIntervalSeconds)
	}
	if cfg.LogFile != "auto_pull.log" {
		t.Errorf("LogFile = %q, want auto_pull.log", cfg.LogFile)
	}
}

func TestLoadConfig_CustomValues(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{
		"repo_path":              dir,
		"branch":                 "develop",
		"check_interval_seconds": 30,
		"log_file":               "custom.log",
		"git_recovery_mode":      "stash",
	})
	cfg, err := autopull.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Branch != "develop" {
		t.Errorf("Branch = %q, want develop", cfg.Branch)
	}
	if cfg.CheckIntervalSeconds != 30 {
		t.Errorf("CheckIntervalSeconds = %d, want 30", cfg.CheckIntervalSeconds)
	}
	if cfg.GitRecoveryMode != "stash" {
		t.Errorf("GitRecoveryMode = %q, want stash", cfg.GitRecoveryMode)
	}
}

func TestLoadConfig_RejectsGithubToken(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{
		"repo_path":    dir,
		"github_token": "ghp_secret",
	})
	_, err := autopull.LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "github_token") {
		t.Fatalf("expected github_token error, got %v", err)
	}
}

func TestLoadConfig_RejectsReposField(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{
		"repo_path": dir,
		"repos":     []any{},
	})
	_, err := autopull.LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "repos") {
		t.Fatalf("expected repos error, got %v", err)
	}
}

func TestLoadConfig_TokenFromDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{"repo_path": dir})
	writeEnvFile(t, dir, "AUTOPULL_TOKEN=envfiletoken\n")
	cfg, err := autopull.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GithubToken != "envfiletoken" {
		t.Errorf("GithubToken = %q, want envfiletoken", cfg.GithubToken)
	}
}

func TestLoadConfig_TokenFromEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{"repo_path": dir})
	t.Setenv("AUTOPULL_TOKEN", "envvartoken")
	cfg, err := autopull.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GithubToken != "envvartoken" {
		t.Errorf("GithubToken = %q, want envvartoken", cfg.GithubToken)
	}
}

func TestLoadConfig_TokenNotInJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{"repo_path": dir})
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "github_token") || strings.Contains(string(data), "GithubToken") {
		t.Error("config JSON must not contain token field")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := autopull.LoadConfig("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

// Regression: an empty or relative repo_path used to be FATAL ("repo_path is
// required") or resolved against the process cwd ("/" under systemd), which
// broke any config that was committed or copied between machines.
func TestLoadConfig_EmptyRepoPathUsesConfigDir(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, map[string]any{"branch": "main"})
	cfg, err := autopull.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RepoPath != dir {
		t.Errorf("RepoPath = %q, want %q", cfg.RepoPath, dir)
	}
}

func TestLoadConfig_RelativeRepoPathIsRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "deploy")
	os.MkdirAll(sub, 0755)
	path := writeConfig(t, sub, map[string]any{"repo_path": "..", "post_pull_workdir": "web"})

	orig, _ := os.Getwd()
	os.Chdir(t.TempDir()) // cwd must not matter
	defer os.Chdir(orig)

	cfg, err := autopull.LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RepoPath != dir {
		t.Errorf("RepoPath = %q, want %q", cfg.RepoPath, dir)
	}
	if want := filepath.Join(dir, "web"); cfg.PostPullWorkdir != want {
		t.Errorf("PostPullWorkdir = %q, want %q", cfg.PostPullWorkdir, want)
	}
}

// LoadConfig is called on every tick; it must never rewrite the file (a
// rewritten committed config is what blocked every later pull).
func TestLoadConfig_DoesNotModifyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config_auto_pull.json")
	orig := []byte("{\"branch\":\"main\"}")
	os.WriteFile(path, orig, 0644)
	if _, err := autopull.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(orig) {
		t.Errorf("config was modified: %q", got)
	}
}

func TestLoadDotEnvToken_ExportPrefix(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir, "export AUTOPULL_TOKEN=tok\n")
	if got := autopull.LoadDotEnvToken(dir); got != "tok" {
		t.Errorf("got %q, want tok", got)
	}
}

// ─── helpers ─────────────────────────────────

func writeConfig(t *testing.T, dir string, content map[string]any) string {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config_auto_pull.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeEnvFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
