package autopull

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatch_StopsWhenRepoDeleted(t *testing.T) {
	_, srv := setupClone(t)
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config_auto_pull.json")
	data, _ := json.Marshal(map[string]any{
		"repo_path":              srv,
		"branch":                 "main",
		"check_interval_seconds": 1,
		"log_file":               filepath.Join(cfgDir, "auto_pull.log"),
		"notify_on_pull":         false,
	})
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- watch(ctx, cfgPath) }()

	time.Sleep(1500 * time.Millisecond)
	if err := os.RemoveAll(srv); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-done:
		if code != ExitConfig {
			t.Fatalf("exit code = %d, want %d", code, ExitConfig)
		}
	case <-ctx.Done():
		t.Fatal("watcher kept running after repo_path was deleted")
	}
	if _, err := os.Stat(pidFilePath(cfgPath)); !os.IsNotExist(err) {
		t.Error("pid file should be removed on exit")
	}
}

func TestMissingPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.json")
	os.WriteFile(cfg, []byte("{}"), 0644)
	if got := missingPath(cfg, dir); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := missingPath(cfg, filepath.Join(dir, "nope")); got == "" {
		t.Error("missing repo_path not reported")
	}
	if got := missingPath(filepath.Join(dir, "nope.json"), dir); got == "" {
		t.Error("missing config not reported")
	}
}
