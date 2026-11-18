package decide

// DirectChildren is a bounded structured direct-child snapshot. It is separate
// from diagnostic IDs and does not classify transcript or title text.
type DirectChildren struct {
	Complete bool
	States   map[string]ChildState
}
type ChildState struct {
	Execution          string
	PendingInteraction bool
}
