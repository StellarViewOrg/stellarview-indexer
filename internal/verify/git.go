package verify

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var gitCommitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ValidateGitSource(src SourceSpec) error {
	if src.Repository == "" {
		return fmt.Errorf("%w: git repository is required", ErrInvalidRequest)
	}
	u, err := url.Parse(src.Repository)
	if err != nil {
		return fmt.Errorf("%w: invalid git repository URL", ErrInvalidRequest)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: git repository must use https", ErrInvalidRequest)
	}
	if u.User != nil || u.Host == "" {
		return fmt.Errorf("%w: invalid git repository URL", ErrInvalidRequest)
	}
	if len(u.Host) > 255 || len(src.Repository) > 512 {
		return fmt.Errorf("%w: git repository URL too long", ErrInvalidRequest)
	}
	if !gitCommitRe.MatchString(strings.ToLower(src.Commit)) {
		return fmt.Errorf("%w: commit must be a full 40-character hex SHA", ErrInvalidRequest)
	}
	return nil
}

var fetchGitSourceFunc = fetchGitSource

func fetchGitSource(ctx context.Context, src SourceSpec, dest string) error {
	if err := ValidateGitSource(src); err != nil {
		return err
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git is not available on the indexer host")
	}

	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, gitBin, args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/true")
		cmd.Dir = dest
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %.500s", args[0], err, sanitizeGitOutput(string(out)))
		}
		return nil
	}

	steps := [][]string{
		{"init", "."},
		{"remote", "add", "origin", src.Repository},
		{"fetch", "--depth", "1", "origin", strings.ToLower(src.Commit)},
		{"checkout", "FETCH_HEAD"},
	}
	for _, s := range steps {
		if err := run(s...); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeGitOutput(s string) string {
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
