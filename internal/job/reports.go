package job

type Source struct {
	PartitionKey string `json:"partition_key"`
	FindingID    string `json:"finding_id"`
}
type Finding struct {
	ID        string   `json:"id"`
	Severity  string   `json:"severity"`
	Summary   string   `json:"summary"`
	Path      string   `json:"path"`
	LineStart int      `json:"line_start"`
	LineEnd   int      `json:"line_end"`
	Evidence  string   `json:"evidence"`
	Origin    string   `json:"origin,omitempty"`
	Sources   []Source `json:"sources,omitempty"`
}
type Findings struct {
	Version      string    `json:"schema_version"`
	BaseCommit   string    `json:"base_commit"`
	PartitionKey string    `json:"partition_key"`
	Findings     []Finding `json:"findings"`
}
type MergedReport struct {
	Version      string    `json:"schema_version"`
	BaseCommit   string    `json:"base_commit"`
	ManifestHash string    `json:"manifest_sha256"`
	Summary      string    `json:"summary"`
	Findings     []Finding `json:"findings"`
}
