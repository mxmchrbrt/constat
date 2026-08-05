package assert

import (
	"context"

	"github.com/mxmchrbrt/constat/internal/driver"
)

type Env struct {
	Driver     driver.Driver
	RestoreDir string
}

type Assertion interface {
	Check(ctx context.Context, env Env) (Result, error)
}
