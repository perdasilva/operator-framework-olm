package runtime_constraints

import (
	"context"
	"fmt"
	"github.com/blang/semver/v4"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/operators/openshift"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/cache"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"strings"
	"sync"
)

const (
	RuntimeConstraintEnvVarName = "RUNTIME_CONSTRAINTS"
)

type OCPRuntimeConstraintProvider struct {
	clusterProperties ClusterProperties
}

func NewOpenShiftRuntimeConstraintsProvider(cli configv1client.ClusterVersionsGetter) (RuntimeConstraintsProvider, error) {
	_, isSet := os.LookupEnv(RuntimeConstraintEnvVarName)
	if !isSet {
		return nil, nil
	}
	clusterProperties := NewOCPClusterProperties(cli)
	return &OCPRuntimeConstraintProvider{
		clusterProperties: clusterProperties,
	}, nil
}

func (o *OCPRuntimeConstraintProvider) Constraints() ([]cache.Predicate, error) {
	// change this to use the generic constraint type
	clusterVersion, err := o.clusterProperties.Version()
	if err != nil {
		return nil, err
	}
	return []cache.Predicate{NewMaxOCPVersionPredicate(clusterVersion)}, nil
}

type ClusterProperties interface {
	Version() (semver.Version, error)
}

type OCPClusterProperties struct {
	mu  sync.Mutex
	ver *semver.Version
	cli configv1client.ClusterVersionsGetter
}

func NewOCPClusterProperties(cli configv1client.ClusterVersionsGetter) *OCPClusterProperties {
	return &OCPClusterProperties{
		mu:  sync.Mutex{},
		cli: cli,
	}
}

func (o *OCPClusterProperties) Version() (semver.Version, error) {
	var (
		err error
		ver semver.Version
	)

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.ver == nil {
		// Note: We lazy-load cluster.ver so instantiating a cluster struct doesn't require
		// a running OpenShift cluster; i.e. we don't want command options like --version failing
		// because we can't talk to a cluster.
		o.ver, err = o.desiredVersion()
	}

	if o.ver != nil {
		ver = *o.ver
	}

	return ver, err
}

func (o *OCPClusterProperties) desiredVersion() (*semver.Version, error) {
	if o.cli == nil {
		return nil, fmt.Errorf("nil client")
	}

	var desired semver.Version
	cv, err := o.cli.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	if err != nil { // "version" is the name of OpenShift's ClusterVersion singleton
		return nil, fmt.Errorf("failed to get cluster version: %w", err)
	}

	if cv == nil {
		// Note: A nil return without an error breaks the client's contract.
		// If this happens it's probably due to a client fake with ill-defined behavior.
		// TODO(njhale): Should this panic to indicate developer error?
		return nil, fmt.Errorf("incorrect client behavior observed")
	}

	v := cv.Status.Desired.Version
	if v == "" {
		// The release version hasn't been set yet
		return nil, fmt.Errorf("desired release missing from resource")
	}

	desired, err = semver.ParseTolerant(v)
	if err != nil {
		return nil, fmt.Errorf("resource has invalid desired release: %w", err)
	}

	return &desired, nil
}

func maxOpenShiftVersion(entry *cache.Entry) (*semver.Version, error) {
	// Get the max property -- if defined -- and check for duplicates
	var max *string
	for _, property := range entry.Properties {
		if property.Type != openshift.MaxOpenShiftVersionProperty {
			continue
		}

		if max != nil {
			return nil, fmt.Errorf("defining more than one %q property is not allowed", openshift.MaxOpenShiftVersionProperty)
		}

		max = &property.Value
	}

	if max == nil {
		return nil, nil
	}

	// Account for any additional quoting
	value := strings.Trim(*max, "\"")
	if value == "" {
		// Handle "" separately, so parse doesn't treat it as a zero
		return nil, fmt.Errorf(`value cannot be "" (an empty string)`)
	}

	version, err := semver.ParseTolerant(value)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %q as semver: %w", value, err)
	}

	truncatedVersion := semver.Version{Major: version.Major, Minor: version.Minor}
	if !version.EQ(truncatedVersion) {
		return nil, fmt.Errorf("property %q must specify only <major>.<minor> version, got invalid value %s", openshift.MaxOpenShiftVersionProperty, version)
	}
	return &truncatedVersion, nil
}

// TODO: remove this and use Vu's generic constraint predicate instead
type maxOCPVersionPredicate struct {
	version semver.Version
}

func (m maxOCPVersionPredicate) Test(entry *cache.Entry) bool {
	maxOCPVersion, err := maxOpenShiftVersion(entry)

	// if the property value could not be determined, exclude the entry
	if err != nil {
		fmt.Printf("Error getting property %s from entry %s", openshift.MaxOpenShiftVersionProperty, entry.Name)
		return false
	}

	// if the maxOCPVersion property is undefined include the entry
	if maxOCPVersion == nil {
		return true
	}

	return maxOCPVersion.GTE(m.version)
}

func (m maxOCPVersionPredicate) String() string {
	return fmt.Sprintf("with OpenShift version <= %s", string(m.version.String()))
}

func NewMaxOCPVersionPredicate(version semver.Version) cache.Predicate {
	return &maxOCPVersionPredicate{
		version: version,
	}
}
