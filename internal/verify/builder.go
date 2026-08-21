package verify

import (
	"context"
	"fmt"
)

type BuildRequest struct {
	ID        string
	SourceDir string
	OutputDir string
	Spec      BuildSpec
}

type BuildResult struct {
	Wasm      []byte
	Toolchain Toolchain
	Logs      string
}

type BuildError struct {
	Msg  string
	Logs string
}

func (e *BuildError) Error() string { return e.Msg }

func newBuildError(logs, format string, args ...interface{}) *BuildError {
	return &BuildError{Msg: fmt.Sprintf(format, args...), Logs: logs}
}

type Builder interface {
	Build(ctx context.Context, req BuildRequest) (*BuildResult, error)
}
