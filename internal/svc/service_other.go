//go:build !windows

package svc

import (
	"context"
	"os/signal"
	"syscall"
)

// runService runs fn until the service manager (systemd, launchd) stops us.
func runService(name string, fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return fn(ctx)
}
