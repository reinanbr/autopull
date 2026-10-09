package autopull

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ownFile is one of autopull's own runtime files (config, state, pid, log)
// that lives inside the watched repo's work tree.
type ownFile struct {
	abs string // absolute path on disk
	rel string // path relative to the repo top-level, slash-separated
	// guard marks files whose content is preserved across merges and resets.
	// Only files autopull reopens by path are safe to rewrite: the pid file
	// (its fd holds the instance flock) and the log (held open for append)
	// would silently detach from their open descriptors.
	guard bool
}

// realPath resolves symlinks in p's directory so it can be compared against
// `git rev-parse --show-toplevel`, which always returns a resolved path.
func realPath(p string) string {
	dir, base := filepath.Dir(p), filepath.Base(p)
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return filepath.Join(dir, base)
}

func repoTopLevel(repoPath string) (string, error) {
	top, err := runGit(repoPath, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	return top, nil
}

// ownFilesInRepo returns the repo top-level and those of autopull's own files
// that sit inside it. Files outside the work tree (e.g. a config in
// /etc/autopull) can never interfere with git and are left out.
func ownFilesInRepo(cfg *Config, cfgPath string) (string, []ownFile) {
	top, err := repoTopLevel(cfg.RepoPath)
	if err != nil {
		return "", nil
	}
	candidates := []struct {
		path  string
		guard bool
	}{
		{cfgPath, true},
		{stateFilePath(cfgPath), true},
		{pidFilePath(cfgPath), false},
		{resolveLogPath(cfg, cfgPath), false},
	}
	seen := map[string]bool{}
	var files []ownFile
	for _, c := range candidates {
		abs := realPath(c.path)
		rel, err := filepath.Rel(top, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		files = append(files, ownFile{abs: abs, rel: rel, guard: c.guard})
	}
	return top, files
}

func ownRels(files []ownFile) []string {
	rels := make([]string, 0, len(files))
	for _, f := range files {
		rels = append(rels, f.rel)
	}
	return rels
}

// trackedInIndex returns those of files (paths relative to dir) that git tracks.
func trackedInIndex(dir string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"ls-files", "--"}, files...)
	out, err := runGit(dir, "", args...)
	if err != nil || out == "" {
		return nil
	}
	var tracked []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			tracked = append(tracked, f)
		}
	}
	return tracked
}

// statusPath extracts the path from a `git status --porcelain` line as
// returned by TrackedChanges (which trims leading whitespace). For renames it
// returns the destination path.
func statusPath(line string) string {
	fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
	if len(fields) != 2 {
		return ""
	}
	p := strings.TrimSpace(fields[1])
	if i := strings.Index(p, " -> "); i >= 0 {
		p = p[i+4:]
	}
	return strings.Trim(p, `"`)
}

// filterOwnChanges drops autopull's own files from a dirty-file list. They
// are handled by guardOwnFiles around every merge/reset instead, so a locally
// edited, committed config must not stall the watcher as "dirty".
func filterOwnChanges(changes []string, files []ownFile) []string {
	if len(files) == 0 {
		return changes
	}
	own := map[string]bool{}
	for _, f := range files {
		own[f.rel] = true
	}
	out := changes[:0:0]
	for _, line := range changes {
		if !own[statusPath(line)] {
			out = append(out, line)
		}
	}
	return out
}

// prepareOwnFiles runs once at watcher startup:
//
//   - clears any --assume-unchanged bit that older autopull versions set on
//     its own tracked files. That bit hid real local edits from git, which is
//     exactly what made `git pull` fail later with "local changes would be
//     overwritten by merge" while every status check claimed the tree was
//     clean. Own files are now filtered explicitly and guarded instead.
//   - adds the untracked ones to .git/info/exclude (local, never committed) so
//     a `git add -A` in the repo can't accidentally commit them.
//   - logs a one-time hint for any that are tracked.
func prepareOwnFiles(cfg *Config, cfgPath string, l *logger) {
	top, files := ownFilesInRepo(cfg, cfgPath)
	if len(files) == 0 {
		return
	}
	tracked := trackedInIndex(top, ownRels(files))
	if len(tracked) > 0 {
		args := append([]string{"update-index", "--no-assume-unchanged", "--"}, tracked...)
		_, _ = runGit(top, "", args...)
	}
	if err := ensureLocalExcludes(top, files, tracked); err != nil && l != nil {
		l.warn(fmt.Sprintf("could not update .git/info/exclude: %v", err))
	}
	if hints := OwnFileTrackingHints(cfg, cfgPath); len(hints) > 0 && l != nil {
		l.warn("autopull's own file(s) are committed in this repo. autopull protects them during " +
			"pulls, but untracking them is cleaner. Run once, from the repo root:")
		for _, cmd := range hints {
			l.warn("  " + cmd)
		}
	}
}

// ensureLocalExcludes appends untracked own files to the repo's
// info/exclude file, skipping entries already present.
func ensureLocalExcludes(top string, files []ownFile, tracked []string) error {
	excludePath, err := runGit(top, "", "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(top, excludePath)
	}
	isTracked := map[string]bool{}
	for _, t := range tracked {
		isTracked[t] = true
	}
	existing, _ := os.ReadFile(excludePath)
	present := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, f := range files {
		pattern := "/" + f.rel
		if isTracked[f.rel] || present[pattern] {
			continue
		}
		add = append(add, pattern)
	}
	if len(add) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		buf.WriteByte('\n')
	}
	if !present["# autopull runtime files"] {
		buf.WriteString("# autopull runtime files\n")
	}
	for _, p := range add {
		buf.WriteString(p + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf.Bytes())
	return err
}

type ownSnapshot struct {
	file      ownFile
	data      []byte
	mode      os.FileMode
	keepLocal bool // local content differs from HEAD/target and must win
	// upstreamChanged: target's version differs from HEAD's, so restoring the
	// local copy overrides a real upstream edit (worth a log line).
	upstreamChanged bool
}

// ownFileGuard preserves autopull's own files across a git operation that
// rewrites the work tree (merge, reset --hard).
type ownFileGuard struct {
	snaps []ownSnapshot
}

// guardOwnFiles prepares own files so that moving the work tree to target
// cannot fail on them or destroy them:
//
//   - tracked + locally modified: snapshot, then restore HEAD's version so
//     the merge isn't refused with "local changes would be overwritten".
//   - untracked, but present in target: snapshot and move aside so the merge
//     isn't refused with "untracked working tree files would be overwritten".
//   - tracked + unmodified: snapshot only, so the file is restored if target
//     deletes it (e.g. after someone commits the recommended `git rm --cached`).
//
// restore() puts local content back afterwards. Without this, a server whose
// config was committed upstream either stopped pulling forever, or lost its
// config the moment upstream untracked it.
func guardOwnFiles(cfg *Config, cfgPath, target string) *ownFileGuard {
	g := &ownFileGuard{}
	top, files := ownFilesInRepo(cfg, cfgPath)
	if len(files) == 0 {
		return g
	}
	tracked := map[string]bool{}
	for _, t := range trackedInIndex(top, ownRels(files)) {
		tracked[t] = true
	}
	for _, f := range files {
		if !f.guard {
			continue
		}
		info, err := os.Stat(f.abs)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(f.abs)
		if err != nil {
			continue
		}
		snap := ownSnapshot{file: f, data: data, mode: info.Mode().Perm()}
		if tracked[f.rel] {
			if _, err := runGit(top, "", "diff", "--quiet", "HEAD", "--", f.rel); err != nil {
				if _, err := runGit(top, "", "checkout", "HEAD", "--", f.rel); err != nil {
					continue // leave it alone; the merge will report the problem
				}
				snap.keepLocal = true
				headBlob, _ := runGit(top, "", "rev-parse", "HEAD:"+f.rel)
				targetBlob, _ := runGit(top, "", "rev-parse", target+":"+f.rel)
				snap.upstreamChanged = headBlob != targetBlob
			}
		} else if _, err := runGit(top, "", "cat-file", "-e", target+":"+f.rel); err == nil {
			if err := os.Remove(f.abs); err != nil {
				continue
			}
			snap.keepLocal = true
			snap.upstreamChanged = true
		}
		g.snaps = append(g.snaps, snap)
	}
	return g
}

// restore writes snapshotted content back where needed and returns a
// human-readable note for each file it restored.
func (g *ownFileGuard) restore() []string {
	var notes []string
	for _, s := range g.snaps {
		cur, err := os.ReadFile(s.file.abs)
		missing := os.IsNotExist(err)
		if !missing && (!s.keepLocal || bytes.Equal(cur, s.data)) {
			continue
		}
		if err := os.WriteFile(s.file.abs, s.data, s.mode); err != nil {
			notes = append(notes, fmt.Sprintf("FAILED to restore %s: %v", s.file.rel, err))
			continue
		}
		if missing {
			notes = append(notes, fmt.Sprintf("restored %s (removed by the update; local copy kept)", s.file.rel))
		} else if s.upstreamChanged {
			notes = append(notes, fmt.Sprintf("kept local %s (upstream copy differs; local wins)", s.file.rel))
		}
	}
	return notes
}

// OwnFileTrackingHints returns ready-to-run shell commands (to run from the
// repo root) that permanently untrack any of autopull's own runtime files
// (config, state, pid, log) that are tracked in the repo's git index.
func OwnFileTrackingHints(cfg *Config, cfgPath string) []string {
	top, files := ownFilesInRepo(cfg, cfgPath)
	tracked := trackedInIndex(top, ownRels(files))
	cmds := make([]string, 0, len(tracked))
	for _, f := range tracked {
		cmds = append(cmds, gitignoreUntrackCmd(f, f))
	}
	return cmds
}
