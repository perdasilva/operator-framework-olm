package resolver

import (
	"context"
	"errors"
	"fmt"
	"github.com/blang/semver/v4"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/operators/openshift"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/cache"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/solver"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"strings"
	"sync"
)

const (
	SystemConstraintEnvVarName = "SYSTEM_CONSTRAINTS"
)

var (
	log = logrus.New()
)

func init() {
	initHooks = append(initHooks, openShiftStepResolverInit)
}

// this functions gives unit testing a way to inject clusterProperties
// into the openShiftStepResolverInit hook
var clusterPropertiesBuilder = func() clusterProperties {
	clusterVersionClient := configv1client.NewForConfigOrDie(config.GetConfigOrDie())
	return newOpenShiftClusterProperties(clusterVersionClient)
}

// openShiftStepResolverInit is an init hook for the OperatorStepResolver that
// sets the underlying satResolver's systemConstraintsProvider to a configured
// instance of the openShiftSystemConstraintsProvider. The constraint provider
// adds additional constraints on cache entries that violate cluster properties.
// For instance, if a bundle defines a property: maxOpenShiftVersion = 4.10, the
// constraint provider will prohibit any bundle with maxOpenShiftversion < 4.10
func openShiftStepResolverInit(stepResolver *OperatorStepResolver) error {
	if stepResolver == nil {
		return nil
	}

	if stepResolver.satResolver.systemConstraintsProvider != nil {
		panic("resolver already has a system constraint provider defined")
	}

	if os.Getenv(SystemConstraintEnvVarName) != "true" {
		log.Infoln("System constraints are off")
		return nil
	}

	openShiftClusterProps := clusterPropertiesBuilder()
	ver, err := openShiftClusterProps.Version()
	if err != nil {
		log.Errorln("cannot determine cluster version")
		panic(err)
	}

	log.Info("loading system properties")
	log.Infof("cluster version %s\n", ver)

	// create the system constraints provider and attach it to the sat resolver
	constraintsProvider := newOpenShiftSystemConstraintsProvider(openShiftClusterProps)
	stepResolver.satResolver.systemConstraintsProvider = constraintsProvider
	return nil
}

type openShiftSystemConstraintsProvider struct {
	openshiftClusterProperties clusterProperties
}

func newOpenShiftSystemConstraintsProvider(openshiftClusterProperties clusterProperties) *openShiftSystemConstraintsProvider {
	return &openShiftSystemConstraintsProvider{
		openshiftClusterProperties: openshiftClusterProperties,
	}
}

func (c *openShiftSystemConstraintsProvider) Constraints(entry *cache.Entry) ([]solver.Constraint, error) {
	if c.openshiftClusterProperties == nil {
		return nil, errors.New("nil clusterProperties")
	}

	// Get maxOCPVersion from entry
	maxOCPVersion, err := c.extractMaxOpenShiftVersionPropertyValue(entry)
	if err != nil {
		// All parsing errors should prohibit the entry from being installed
		return []solver.Constraint{PrettyConstraint(
			solver.Prohibited(),
			fmt.Sprintf("invalid %q property: %s", openshift.MaxOpenShiftVersionProperty, err),
		)}, nil // Don't bubble up err -- this allows resolution to continue
	}

	// if maxOCPVersion property is undefined, all is well
	if maxOCPVersion == nil {
		return nil, nil
	}

	// Get cluster version
	clusterVersion, err := c.openshiftClusterProperties.Version()
	if err != nil {
		return nil, err
	}

	// Drop Z, since we only care about compatibility with Y
	clusterVersion.Minor = 0

	// Compare cluster version and return constraint if necessary
	if maxOCPVersion.LT(*clusterVersion) {
		return []solver.Constraint{PrettyConstraint(
			solver.Prohibited(),
			fmt.Sprintf("bundle incompatible with openshift cluster, %q < cluster version: (%d.%d < %d.%d)", openshift.MaxOpenShiftVersionProperty, maxOCPVersion.Major, maxOCPVersion.Minor, clusterVersion.Major, clusterVersion.Minor),
		)}, nil
	}
	return nil, nil
}

func (c *openShiftSystemConstraintsProvider) extractMaxOpenShiftVersionPropertyValue(entry *cache.Entry) (*semver.Version, error) {
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

type clusterProperties interface {
	Version() (*semver.Version, error)
}

type openShiftClusterProperties struct {
	mu  sync.Mutex
	ver *semver.Version
	cli configv1client.ClusterVersionsGetter
}

func newOpenShiftClusterProperties(cli configv1client.ClusterVersionsGetter) clusterProperties {
	return &openShiftClusterProperties{
		mu:  sync.Mutex{},
		cli: cli,
	}
}

func (o *openShiftClusterProperties) Version() (*semver.Version, error) {
	var (
		err error
		ver *semver.Version
	)

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.ver == nil {
		// Note: We lazy-load cluster.ver so instantiating a cluster struct doesn't require
		// a running OpenShift cluster; i.e. we don't want command options like --version failing
		// because we can't talk to a cluster.
		o.ver, err = o.queryClusterVersion()
	}

	if o.ver != nil {
		ver = o.ver
	}

	return ver, err
}

func (o *openShiftClusterProperties) queryClusterVersion() (*semver.Version, error) {
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
