package main

import (
	"context"
	"errors"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/source/gmail"
)

// errAuthUsage is returned when `renomail auth` is invoked without an account
// argument. dispatch turns it into the process error message.
var errAuthUsage = errors.New("usage: renomail auth <account@gmail.com>")

// authAccount returns the account argument for `renomail auth`, or "" when it was
// omitted (which runAuth turns into a usage error).
func authAccount(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// runAuth runs the one-time Gmail consent flow for account and writes its token,
// so subsequent headless runs can refresh access without the browser. It is the
// thin command wrapper over gmail.Authorize.
func runAuth(ctx context.Context, paths config.Paths, account string) error {
	if account == "" {
		return errAuthUsage
	}
	return gmail.Authorize(ctx, paths, account)
}
