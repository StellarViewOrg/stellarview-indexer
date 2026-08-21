package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite golden WASM hash files")

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "info").Run() == nil
}

func builderImagePresent(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

func TestDockerRunArgsEnforceSandbox(t *testing.T) {
	b := NewDockerBuilder("stellarview/soroban-builder:test")
	args := b.runArgs("sv-verify-abc", BuildRequest{
		ID:        "abc",
		SourceDir: "/tmp/job/src",
		OutputDir: "/tmp/job/out",
	})
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--network=none",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt no-new-privileges",
		"--pids-limit 256",
		"--memory 2g",
		"--user 1000:1000",
		"/src:ro",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("sandbox arg %q missing from docker run args: %v", required, args)
		}
	}
}

// TestGoldenReproducibleBuild is the golden-file reproducibility check:
// a known contract source (testdata/golden-contract) must build to a stable,
// committed WASM hash. Requires Docker and the builder image; skips otherwise.
// Regenerate the golden file after intentional toolchain bumps with:
//
//	go test ./internal/verify/ -run TestGoldenReproducibleBuild -update-golden
func TestGoldenReproducibleBuild(t *testing.T) {
	const image = "stellarview/soroban-builder:latest"

	if !dockerAvailable() {
		t.Skip("skipping: docker not available")
	}
	if !builderImagePresent(image) {
		t.Skipf("skipping: builder image %s not present (build it with `make builder-image`)", image)
	}

	srcRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "golden-contract"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(srcRoot, "Cargo.toml")); err != nil {
		t.Skipf("skipping: golden contract source missing: %v", err)
	}

	b := NewDockerBuilder(image)

	buildHash := func(tag string) string {
		t.Helper()
		outDir := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()

		res, err := b.Build(ctx, BuildRequest{
			ID:        "golden-" + tag,
			SourceDir: srcRoot,
			OutputDir: outDir,
			Spec:      BuildSpec{Profile: ProfileRelease},
		})
		if err != nil {
			t.Fatalf("golden build %s failed: %v", tag, err)
		}
		sum := sha256.Sum256(res.Wasm)
		return hex.EncodeToString(sum[:])
	}

	first := buildHash("first")
	second := buildHash("second")
	if first != second {
		t.Fatalf("build is not reproducible: %s != %s", first, second)
	}

	goldenPath := filepath.Join("..", "..", "testdata", "golden-contract", "golden_wasm_hash.txt")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(first+"\n"), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden hash updated: %s", first)
		return
	}

	goldenBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Skipf("skipping: no golden file yet (generate with -update-golden): %v", err)
	}
	golden := strings.TrimSpace(string(goldenBytes))
	if first != golden {
		t.Fatalf("built hash %s does not match golden %s — toolchain drift or tampered source", first, golden)
	}
}
