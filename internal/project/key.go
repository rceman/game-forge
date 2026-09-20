package project

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// CanonicalRoot resolves symlinks and absolutizes a project root so the same
// checkout always hashes identically.
func CanonicalRoot(root string) string {
	if p, err := filepath.EvalSymlinks(root); err == nil {
		root = p
	}
	if p, err := filepath.Abs(root); err == nil {
		root = p
	}
	return filepath.Clean(root)
}

// KeyFor derives the stable per-checkout project identity: the human project
// id plus a short hash of the canonical project root, e.g.
// "spin-tower-a81c92". Two checkouts may share a project.id but never share a
// key, so daemon-owned resources stay project-isolated.
func KeyFor(m *Manifest) string {
	root := CanonicalRoot(m.Root)
	sum := sha256.Sum256([]byte(root))
	return fmt.Sprintf("%s-%x", sanitize(m.Project.ID), sum[:3])
}

// sanitize reduces a project id to characters safe for a provider namespace.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "project"
	}
	return out
}
