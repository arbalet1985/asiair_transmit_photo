//go:build !windows

package config

func protect(b []byte) ([]byte, error) { return nil, errNoProtect }

func unprotect(b []byte) ([]byte, error) { return nil, errNoProtect }
