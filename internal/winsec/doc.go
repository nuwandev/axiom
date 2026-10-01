//go:build !windows

// Package winsec implements Windows-native filesystem security checks. It
// has no unix implementation; this file exists only so "go build ./..."
// does not fail on a package with no buildable files for the target OS.
package winsec
