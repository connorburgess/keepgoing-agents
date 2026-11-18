package bb

import (
	"context"
	"github.com/looptender/looptender/internal/decide"
)

// AssignmentChildren revalidates only the existing typed child scan. It never
// reads logs or sends to children. The supervisor supplies an optional budget.
func (a *Adapter) AssignmentChildren(ctx context.Context) *decide.DirectChildren {
	a.childWork(ctx)
	return a.directChildren
}
