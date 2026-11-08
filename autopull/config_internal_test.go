package autopull

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteConfig_DoesNotEscapeShellOperators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config_auto_pull.json")
	cmd := "./deploy.sh >> log_deploy.log 2>&1 && echo <done>"
	if err := writeConfig(path, configFile{PostPullCommand: cmd}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), cmd) {
		t.Errorf("post_pull_command was escaped:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("config must end with a single newline:\n%q", data)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PostPullCommand != cmd {
		t.Errorf("round-trip mismatch: %q", cfg.PostPullCommand)
	}
}

func TestOriginIsSSH(t *testing.T) {
	cases := map[string]bool{
		"git@github.com:owner/repo.git":       true,
		"ssh://git@github.com/owner/repo.git": true,
		"https://github.com/owner/repo.git":   false,
		"https://user@github.com/owner/repo":  false,
		"/srv/git/repo.git":                   false,
	}
	for url, want := range cases {
		dir := t.TempDir()
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", url).CombinedOutput(); err != nil {
			t.Fatalf("remote add: %v %s", err, out)
		}
		if got := originIsSSH(dir); got != want {
			t.Errorf("originIsSSH(%q) = %v, want %v", url, got, want)
		}
	}
	if originIsSSH(t.TempDir()) {
		t.Error("a directory without origin must not be reported as SSH")
	}
}
