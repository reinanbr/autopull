package autopull

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// htmlEscapes are the sequences Go's default JSON encoder writes for <, > and
// &. autopull < v1.2.14 rewrote configs that way, turning a post_pull_command
// like "./deploy.sh >> log 2>&1" into "./deploy.sh \u003e\u003e log 2\u003e\u00261".
var htmlEscapes = []struct{ esc, plain string }{
	{`\u0026`, "&"}, {`\u003c`, "<"}, {`\u003e`, ">"},
	{`\u003C`, "<"}, {`\u003E`, ">"},
}

// repairConfigFile undoes damage older autopull versions did to the config
// file, without ever changing what the config means:
//
//   - tracked in git, and the local copy is equivalent to HEAD's (same
//     settings, no keys HEAD lacks): restore HEAD's copy. This covers HTML
//     escapes, reformatting, a dropped trailing newline and dropped keys
//     autopull doesn't read, and leaves `git status` clean again.
//   - otherwise, if it contains &/</>: write those characters
//     back literally, keeping key order, formatting and unknown keys.
//
// A real local edit (a setting that differs from HEAD) is never touched.
// It returns one human-readable note per repair made.
func repairConfigFile(cfgPath, repoPath string) ([]string, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var local configFile
	var localRaw map[string]interface{}
	if json.Unmarshal(data, &local) != nil || json.Unmarshal(data, &localRaw) != nil {
		return nil, nil // invalid JSON is LoadConfig's to report, not ours to guess at
	}

	if note, done := restoreEquivalentFromHEAD(cfgPath, repoPath, local, localRaw); done {
		return []string{note}, nil
	}

	fixed := data
	for _, e := range htmlEscapes {
		fixed = bytes.ReplaceAll(fixed, []byte(e.esc), []byte(e.plain))
	}
	if bytes.Equal(fixed, data) {
		return nil, nil
	}
	// Only keep the rewrite if it decodes to exactly the same value. This
	// rejects e.g. a literal `\\u0026` (an escaped backslash), which the
	// blind replace would turn into an invalid `\&`.
	var before, after interface{}
	if json.Unmarshal(data, &before) != nil || json.Unmarshal(fixed, &after) != nil ||
		!reflect.DeepEqual(before, after) {
		return nil, nil
	}
	if !bytes.HasSuffix(fixed, []byte("\n")) {
		fixed = append(fixed, '\n')
	}
	if err := writeFileAtomic(cfgPath, fixed); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("repaired %s: decoded \\u0026/\\u003c/\\u003e back to &, <, > "+
		"(written by autopull < v1.2.14; settings unchanged)", filepath.Base(cfgPath))}, nil
}

// restoreEquivalentFromHEAD restores the config from HEAD when it is tracked,
// locally modified, and the local copy is equivalent to HEAD's.
func restoreEquivalentFromHEAD(cfgPath, repoPath string, local configFile, localRaw map[string]interface{}) (string, bool) {
	top, err := repoTopLevel(repoPath)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(top, realPath(cfgPath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if len(trackedInIndex(top, []string{rel})) == 0 {
		return "", false
	}
	if _, err := runGit(top, "", "diff", "--quiet", "HEAD", "--", rel); err == nil {
		return "", false // identical to HEAD already
	}
	headData, err := runGit(top, "", "show", "HEAD:"+rel)
	if err != nil {
		return "", false
	}
	var head configFile
	var headRaw map[string]interface{}
	if json.Unmarshal([]byte(headData), &head) != nil || json.Unmarshal([]byte(headData), &headRaw) != nil {
		return "", false
	}
	if local != head {
		return "", false // a real local setting: local wins, leave it alone
	}
	for k := range localRaw {
		if _, ok := headRaw[k]; !ok {
			return "", false // local adds a key: someone put it there on purpose
		}
		if !reflect.DeepEqual(localRaw[k], headRaw[k]) {
			return "", false
		}
	}
	if _, err := runGit(top, "", "checkout", "HEAD", "--", rel); err != nil {
		return "", false
	}
	return fmt.Sprintf("repaired %s: restored the committed copy — the local one differed only "+
		"in formatting/escaping/unused keys (settings unchanged)", rel), true
}

// writeFileAtomic replaces path with data, keeping its permissions, so a
// crash mid-write can never leave a truncated config behind.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
