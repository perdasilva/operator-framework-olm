package olm

func init() {
	operatorPlugIns = []OperatorPlugin{
		// labels unlabelled non-payload openshift-* csv namespaces with
		// security.openshift.io/scc.podSecurityLabelSync: true
		&csvNamespaceLabelerPlugin{},
	}
}
