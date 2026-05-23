package cli

import (
	"context"

	"github.com/samansartipi/curio/internal/version"
)

type ctxKey struct{}

func setCtx(parent context.Context, c *Context) context.Context {
	return context.WithValue(parent, ctxKey{}, c)
}

func getCtx(parent context.Context) (*Context, bool) {
	c, ok := parent.Value(ctxKey{}).(*Context)
	return c, ok
}

func versionString() string { return version.String() }
