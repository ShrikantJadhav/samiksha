package diffsource

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Source describes where to read the diff under review.
type Source struct {
	Base   string
	Patch  string
	Staged bool
	Dir    string // working directory for git commands; empty means current
}

// Read returns the unified diff text and a short label describing its origin.
func (s Source) Read() (string, string, error) {
	switch {
	case s.Patch == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", "", err
		}
		return string(b), "stdin", nil
	case s.Patch != "":
		b, err := os.ReadFile(s.Patch)
		if err != nil {
			return "", "", err
		}
		return string(b), s.Patch, nil
	case s.Staged:
		out, err := git(s.Dir, "diff", "--no-color", "--cached")
		return out, "index (staged)", err
	default:
		base := s.Base
		if base == "" {
			base = DefaultBase(s.Dir)
		}
		out, err := git(s.Dir, "diff", "--no-color", base+"...HEAD")
		return out, base + "...HEAD", err
	}
}

// DefaultBase picks a sensible base ref: remote default branch, then main,
// master, then the previous commit.
func DefaultBase(dir string) string {
	for _, ref := range []string{"origin/HEAD", "main", "master"} {
		if err := gitRun(dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref
		}
	}
	return "HEAD~1"
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run()
}
