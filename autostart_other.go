//go:build !windows

package main

func setAutostart(_ bool) error { return nil }
