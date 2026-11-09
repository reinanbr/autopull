package autopull

import (
	"strings"
	"testing"
)

func TestPostPullStatusLine(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{RepoPath: "/r"}, "Post-pull: none"},
		{Config{RepoPath: "/r", PostPullCommand: "   "}, "Post-pull: none"},
		{Config{RepoPath: "/r", PostPullCommand: "./deploy.sh"}, "Post-pull: ./deploy.sh (in /r)"},
		{Config{RepoPath: "/r", PostPullCommand: "make", PostPullWorkdir: "/w"}, "Post-pull: make (in /w)"},
	}
	for _, c := range cases {
		if got := postPullStatusLine(&c.cfg); !strings.HasPrefix(got, c.want) {
			t.Errorf("postPullStatusLine(%+v) = %q, want prefix %q", c.cfg, got, c.want)
		}
	}
}
