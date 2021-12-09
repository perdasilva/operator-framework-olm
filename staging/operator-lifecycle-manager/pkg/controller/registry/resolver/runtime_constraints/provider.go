package runtime_constraints

import (
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/cache"
)

type RuntimeConstraintsProvider interface {
	Constraints() ([]cache.Predicate, error)
}
