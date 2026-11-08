package autopull

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	RepoPath             string `json:"repo_path"`
	Branch               string `json:"branch"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	PostPullCommand      string `json:"post_pull_command"`
	PostPullWorkdir      string `json:"post_pull_workdir"`
	LogFile              string `json:"log_file"`
	NotifyOnPull         bool   `json:"notify_on_pull"`
	GitRecoveryMode      string `json:"git_recovery_mode"`

	// GithubToken is intentionally excluded from JSON serialization.
	// Set via AUTOPULL_TOKEN or GITHUB_TOKEN env var, or .env file in repo_path.
	GithubToken string `json:"-"`
}

// configFile is the on-disk representation — mirrors Config but omits the token.
type configFile struct {
	RepoPath             string `json:"repo_path"`
	Branch               string `json:"branch"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	PostPullCommand      string `json:"post_pull_command"`
	PostPullWorkdir      string `json:"post_pull_workdir"`
	LogFile              string `json:"log_file"`
	NotifyOnPull         bool   `json:"notify_on_pull"`
	GitRecoveryMode      string `json:"git_recovery_mode"`
}

func LoadDotEnvToken(baseDir string) string {
	f, err := os.Open(filepath.Join(baseDir, ".env"))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(strings.TrimPrefix(parts[0], "export "))
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key == "AUTOPULL_TOKEN" || key == "GITHUB_TOKEN" {
			return val
		}
	}
	if scanner.Err() != nil {
		return ""
	}
	return ""
}

func tokenFromEnv() string {
	if v := os.Getenv("AUTOPULL_TOKEN"); v != "" {
		return v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return v
	}
	return ""
}

func ResolveConfigPath(p string) string {
	if p == "" {
		p = "config_auto_pull.json"
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// reject legacy multi-repo configs explicitly
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if _, hasRepos := raw["repos"]; hasRepos {
		return nil, fmt.Errorf(
			"'repos' field is not supported: each repository should have its own config_auto_pull.json. " +
				"Run 'autopull init' inside each repo directory",
		)
	}
	if _, hasToken := raw["github_token"]; hasToken {
		return nil, fmt.Errorf(
			"'github_token' must not be set in config_auto_pull.json — " +
				"use AUTOPULL_TOKEN or GITHUB_TOKEN in a .env file or environment variable instead",
		)
	}

	var cf configFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg := &Config{
		RepoPath:             cf.RepoPath,
		Branch:               cf.Branch,
		CheckIntervalSeconds: cf.CheckIntervalSeconds,
		PostPullCommand:      cf.PostPullCommand,
		PostPullWorkdir:      cf.PostPullWorkdir,
		LogFile:              cf.LogFile,
		NotifyOnPull:         cf.NotifyOnPull,
		GitRecoveryMode:      NormalizeRecoveryMode(cf.GitRecoveryMode),
	}

	// repo_path and post_pull_workdir are resolved relative to the config
	// file's directory, not the process cwd — the cwd is "/" under systemd and
	// arbitrary under `autopull daemon`. An empty repo_path means "the repo
	// this config lives in", which keeps a committed config portable across
	// machines instead of pinning one host's absolute path.
	cfgDir := filepath.Dir(path)
	if abs, err := filepath.Abs(cfgDir); err == nil {
		cfgDir = abs
	}
	if strings.TrimSpace(cfg.RepoPath) == "" {
		cfg.RepoPath = cfgDir
	} else if !filepath.IsAbs(cfg.RepoPath) {
		cfg.RepoPath = filepath.Join(cfgDir, cfg.RepoPath)
	}
	cfg.RepoPath = filepath.Clean(cfg.RepoPath)
	if cfg.PostPullWorkdir != "" && !filepath.IsAbs(cfg.PostPullWorkdir) {
		cfg.PostPullWorkdir = filepath.Join(cfg.RepoPath, cfg.PostPullWorkdir)
	}

	// defaults
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.CheckIntervalSeconds <= 0 {
		cfg.CheckIntervalSeconds = 5
	}
	if cfg.LogFile == "" {
		cfg.LogFile = "auto_pull.log"
	}

	// token resolution: env var → .env in repo dir → .env next to config → empty
	token := tokenFromEnv()
	if token == "" {
		token = LoadDotEnvToken(cfg.RepoPath)
	}
	if token == "" && cfgDir != cfg.RepoPath {
		token = LoadDotEnvToken(cfgDir)
	}
	cfg.GithubToken = token

	return cfg, nil
}

// resolveLogPath returns the absolute log path; a relative log_file is
// relative to the config file's directory.
func resolveLogPath(cfg *Config, cfgPath string) string {
	if filepath.IsAbs(cfg.LogFile) {
		return cfg.LogFile
	}
	return filepath.Join(filepath.Dir(cfgPath), cfg.LogFile)
}

func NormalizeRecoveryMode(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "", "off":
		return "off"
	case "stash":
		return "stash"
	case "hard-reset", "hard_reset", "hardreset":
		return "hard-reset"
	default:
		return "off"
	}
}

func writeConfig(path string, cfg configFile) error {
	// json.MarshalIndent escapes <, > and & as \u003c etc., which turns a
	// post_pull_command like "./deploy.sh >> log 2>&1" into unreadable noise.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func detectBranch(repoPath string) string {
	branch, err := runGit(repoPath, "", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "" || branch == "HEAD" {
		return "main"
	}
	return branch
}
