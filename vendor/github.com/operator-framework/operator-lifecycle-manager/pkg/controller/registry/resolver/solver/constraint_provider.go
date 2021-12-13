package solver

import "github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/cache"

// ConstraintProvider knows how to provide solver constraints for a given cache entry.
type ConstraintProvider interface {
	// Constraints returns a set of solver constraints for a cache entry.
	Constraints(e *cache.Entry) ([]Constraint, error)
}

type ConstraintProviderFunc func(e *cache.Entry) ([]Constraint, error)

func (c ConstraintProviderFunc) Constraints(e *cache.Entry) ([]Constraint, error) {
	return c(e)
}
