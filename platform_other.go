//go:build !windows

package main

import "os"

func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}

func restrictFile(path string) {
	_ = os.Chmod(path, 0o600)
}
