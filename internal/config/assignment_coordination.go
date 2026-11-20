package config

// AssignmentCoordination permits a bounded coordinator wake for explicitly
// declared independent work. It never dispatches or resumes a lane.
type AssignmentCoordination struct {
	Enabled        bool     `yaml:"enabled"`
	MaxEvidenceAge Duration `yaml:"max_evidence_age,omitempty"`
}
