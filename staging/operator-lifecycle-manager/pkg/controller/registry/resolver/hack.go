package resolver

// This file is a hack to allow us to inject different resolver options downstream.
// The current use case is to enable an early version of the cluster runtime constraints
// targeting the MaxOCPVersion property. In the downstream we add a WithRuntimeConstraintsProvider
// option.
// This file should go away once we have either a better way to add deal with downstream only code, e.g.
// make the architecture more pluggable, etc. Or, have a better way to input the cluster properties and constraints.
var resolverOptions []SatSolverOption

func applyGlobalOptions(satResolver *SatResolver) {
	for _, resolverOption := range resolverOptions {
		resolverOption(satResolver)
	}
}
