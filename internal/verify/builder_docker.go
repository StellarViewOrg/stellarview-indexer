package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	dockerMemory     = "2g"
	dockerCPUs       = "2"
	dockerPidsLimit  = "256"
	containerUID     = "1000:1000"
	tmpfsTmp         = "/tmp:rw,exec,nosuid,size=268435456"
	tmpfsBuild       = "/build:rw,exec,nosuid,size=2147483648"
	maxBuildLogBytes = 512 << 10
	containerTimeout = 20 * time.Minute
	imagePullTimeout = 10 * time.Minute
)

type DockerBuilder struct {
	Image     string
	Memory    string
	CPUs      string
	DockerBin string
}

func NewDockerBuilder(image string) *DockerBuilder {
	return &DockerBuilder{
		Image:     image,
		Memory:    dockerMemory,
		CPUs:      dockerCPUs,
		DockerBin: "docker",
	}
}

func (b *DockerBuilder) bin() string {
	if b.DockerBin != "" {
		return b.DockerBin
	}
	return "docker"
}

var containerNameRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func (b *DockerBuilder) Build(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	buildCtx, cancel := context.WithTimeout(ctx, containerTimeout)
	defer cancel()

	name := "sv-verify-" + containerNameRe.ReplaceAllString(req.ID, "-")
	defer b.removeContainer(name)

	if err := b.ensureImage(buildCtx); err != nil {
		return nil, fmt.Errorf("builder image unavailable: %w", err)
	}

	logs := &limitedBuffer{limit: maxBuildLogBytes}
	cmd := exec.CommandContext(buildCtx, b.bin(), b.runArgs(name, req)...)
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.Env = append(os.Environ(), "DOCKER_CLI_EXPERIMENTAL=disabled")

	if err := cmd.Run(); err != nil {
		if buildCtx.Err() == context.DeadlineExceeded || ctx.Err() == context.Canceled {
			return nil, newBuildError(logs.String(), "build timed out or was cancelled")
		}
		return nil, newBuildError(logs.String(), "build failed (exit status)")
	}

	wasm, err := os.ReadFile(filepath.Join(req.OutputDir, "contract.wasm"))
	if err != nil {
		return nil, newBuildError(logs.String(), "builder produced no contract.wasm")
	}

	tc := readToolchainJSON(req.OutputDir)
	return &BuildResult{Wasm: wasm, Toolchain: tc, Logs: logs.String()}, nil
}

func (b *DockerBuilder) runArgs(name string, req BuildRequest) []string {
	noDefault := "false"
	if req.Spec.NoDefaultFeatures {
		noDefault = "true"
	}
	return []string{
		"run", "--rm",
		"--name", name,
		"--network=none",
		"--cpus", b.CPUs,
		"--memory", b.Memory,
		"--memory-swap", b.Memory,
		"--pids-limit", dockerPidsLimit,
		"--read-only",
		"--tmpfs", tmpfsTmp,
		"--tmpfs", tmpfsBuild,
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--user", containerUID,
		"-v", req.SourceDir + ":/src:ro",
		"-v", req.OutputDir + ":/out",
		"-e", "BUILD_PACKAGE=" + req.Spec.Package,
		"-e", "BUILD_CRATE_DIR=" + req.Spec.CrateDir,
		"-e", "BUILD_FEATURES=" + strings.Join(req.Spec.Features, ","),
		"-e", "BUILD_NO_DEFAULT_FEATURES=" + noDefault,
		"-e", "BUILD_FLAGS=" + strings.Join(req.Spec.Flags, " "),
		b.Image,
	}
}

func (b *DockerBuilder) ensureImage(ctx context.Context) error {
	inspect := exec.CommandContext(ctx, b.bin(), "image", "inspect", b.Image)
	if err := inspect.Run(); err == nil {
		return nil
	}
	pullCtx, cancel := context.WithTimeout(ctx, imagePullTimeout)
	defer cancel()
	pull := exec.CommandContext(pullCtx, b.bin(), "pull", b.Image)
	if out, err := pull.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot pull %s: %v: %.500s", b.Image, err, string(out))
	}
	return nil
}

func (b *DockerBuilder) removeContainer(name string) {
	c := exec.Command(b.bin(), "rm", "-f", name)
	_ = c.Run()
}

func readToolchainJSON(outDir string) Toolchain {
	var tc Toolchain
	data, err := os.ReadFile(filepath.Join(outDir, "toolchain.json"))
	if err != nil {
		return tc
	}
	_ = json.Unmarshal(data, &tc)
	return tc
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.buf.Len() >= l.limit {
		return len(p), nil
	}
	remaining := l.limit - l.buf.Len()
	if len(p) > remaining {
		l.buf.Write(p[:remaining])
		return len(p), nil
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) String() string { return l.buf.String() }
