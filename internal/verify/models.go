package verify

import "time"

type Status string

const (
	StatusQueued   Status = "queued"
	StatusBuilding Status = "building"
	StatusVerified Status = "verified"
	StatusMismatch Status = "mismatch"
	StatusFailed   Status = "failed"
)

func (s Status) Terminal() bool {
	return s == StatusVerified || s == StatusMismatch || s == StatusFailed
}

const (
	SourceTypeArchive = "archive"
	SourceTypeGit     = "git"

	ProfileRelease = "release"
)

type Toolchain struct {
	Rust       string `json:"rust,omitempty"`
	SorobanSDK string `json:"soroban_sdk,omitempty"`
	StellarCLI string `json:"stellar_cli,omitempty"`
}

type BuildSpec struct {
	Profile           string   `json:"profile"`
	Package           string   `json:"package,omitempty"`
	CrateDir          string   `json:"crate_dir,omitempty"`
	Features          []string `json:"features,omitempty"`
	NoDefaultFeatures bool     `json:"no_default_features"`
	Flags             []string `json:"flags,omitempty"`
}

type SourceSpec struct {
	Type       string `json:"type"`
	Name       string `json:"name,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Repository string `json:"repository,omitempty"`
	Reference  string `json:"reference,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

type Verification struct {
	ID                 string     `json:"id"`
	ContractID         string     `json:"contract_id"`
	Network            string     `json:"network"`
	Status             Status     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Match              *bool      `json:"match"`
	WasmHash           *string    `json:"wasm_hash"`
	ExpectedWasmHash   *string    `json:"expected_wasm_hash"`
	Source             SourceSpec `json:"source"`
	ToolchainRequested Toolchain  `json:"toolchain_requested"`
	ToolchainActual    *Toolchain `json:"toolchain"`
	Build              BuildSpec  `json:"build"`
	BuildDurationMS    *int64     `json:"-"`
	SubmitterIP        string     `json:"-"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at"`
}

type VerifiedSource struct {
	WasmHash       string
	VerificationID string
	ContractID     string
	Network        string
	FileCount      int
	TotalSize      int64
	VerifiedAt     time.Time
	Toolchain      *Toolchain
	Build          *BuildSpec
	Source         *SourceSpec
}

type SourceFileMeta struct {
	Path string
	Size int64
}

type SourceFile struct {
	Path    string
	Content []byte
}
