# golden-contract

Minimal Soroban contract used by the golden-file build reproducibility test
(`internal/verify/builder_docker_test.go`).

The committed `golden_wasm_hash.txt` pins the SHA-256 of the WASM produced
from this exact source by the pinned builder toolchain
(`infra/docker/builder/Dockerfile`). The test builds it twice inside the
sandbox and asserts both determinism (two identical hashes) and equality with
the golden value.

Regenerate after an intentional toolchain bump:

    make builder-image
    go test ./internal/verify/ -run TestGoldenReproducibleBuild -update-golden

Note: for fully deterministic offline builds the archive should carry a
committed `Cargo.lock`; add one here when regenerating the golden hash.
