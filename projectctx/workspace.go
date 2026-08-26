package projectctx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Locating the project on disk. Ported from github.com/owainlewis/neo,
// internal/workspace — Copyright (c) 2024 Neo Contributors, MIT
// licensed. Kept in this package rather than one of its own: the
// upward walk exists to serve instruction-file discovery, and
// "workspace" would collide with agentloop.Scope.WorkspaceID, which
// means something else entirely (see the package doc).

// Ancestors returns cwd and each parent up to and including the
// repository root — the first ancestor containing a .git entry — or the
// filesystem root when there is no repository. The slice is ordered
// cwd-first.
func Ancestors(cwd string) []string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		dir = cwd
	}
	var dirs []string
	for {
		dirs = append(dirs, dir)
		if isRepoRoot(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the filesystem root without finding a repo
		}
		dir = parent
	}
	return dirs
}

// Root returns the repository root containing cwd, or cwd's absolute
// path when no repository is found.
func Root(cwd string) string {
	dirs := Ancestors(cwd)
	if last := dirs[len(dirs)-1]; isRepoRoot(last) {
		return last
	}
	return dirs[0]
}

// isRepoRoot reports whether dir holds a .git entry. It may be a file
// rather than a directory — that is what a git worktree or submodule
// checkout looks like, and both are legitimate roots.
func isRepoRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// ResolveWithin returns path resolved against root, following symlinks
// far enough to prove the final target stays under it. The path need
// not exist: the nearest existing parent is resolved and the missing
// suffix appended to that real parent, so a not-yet-created file is
// still checked against where it *would* land.
func ResolveWithin(root, path string) (string, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root %s: %w", absRoot, err)
	}
	if path == "" {
		path = absRoot
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(absRoot, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	realPath, err := resolveExistingPrefix(absPath)
	if err != nil {
		return "", err
	}
	realRoot = filepath.Clean(realRoot)
	realPath = filepath.Clean(realPath)
	if realPath == realRoot {
		return realPath, nil
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside project root %s", path, realRoot)
	}
	return realPath, nil
}

// resolveExistingPrefix walks up until it finds a path component that
// exists, resolves that through symlinks, and rejoins the missing
// remainder.
func resolveExistingPrefix(path string) (string, error) {
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); err == nil {
			realDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return "", err
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return "", err
			}
			if rel == "." {
				return realDir, nil
			}
			return filepath.Join(realDir, rel), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no existing parent for %s", path)
		}
	}
}
