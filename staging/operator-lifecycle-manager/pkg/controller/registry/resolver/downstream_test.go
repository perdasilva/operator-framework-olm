package resolver

import (
	"context"
	"errors"
	"github.com/blang/semver/v4"
	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/operators/openshift"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/cache"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/resolver/solver"
	"github.com/operator-framework/operator-registry/pkg/api"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"os"
	"testing"
)

func TestOpenShiftStepResolverInit_PanicsOnNoStepResolver(t *testing.T) {

	var noOpConstraintProvider solver.ConstraintProviderFunc = func(e *cache.Entry) ([]solver.Constraint, error) {
		return nil, nil
	}

	// downstream.go's clusterPropertiesBuilder variable is a function that builds
	// the clusterProperties object using openshift's cluster client.
	// Since we cannot inject the client or the cluster properties in the
	// hook (because of its method signature), the clusterPropertiesBuilder
	// function is provided as a way to inject a clusterProperties mock
	oldClusterPropertiesBuilder := clusterPropertiesBuilder
	defer func() {
		// set it back again
		clusterPropertiesBuilder = oldClusterPropertiesBuilder
	}()

	testCases := []struct {
		title          string
		stepResolver   *OperatorStepResolver
		clusterVersion *semver.Version
		expectedOutput error
		expectPanic    bool
		isEnvVarSet    bool
	}{
		{
			title:          "returns nil when stepResolver is nil",
			stepResolver:   nil,
			expectedOutput: nil,
			isEnvVarSet:    true,
		},
		{
			title: "panics if system constraint provider is already set",
			stepResolver: &OperatorStepResolver{
				satResolver: &SatResolver{
					systemConstraintsProvider: noOpConstraintProvider,
				},
			},
			expectPanic: true,
			isEnvVarSet: true,
		},
		{
			title: "returns nil if env var is _not_ set to true",
			stepResolver: &OperatorStepResolver{
				satResolver: &SatResolver{},
			},
			isEnvVarSet:    false,
			expectedOutput: nil,
		},
		{
			title: "happy path",
			stepResolver: &OperatorStepResolver{
				satResolver: &SatResolver{},
			},
			isEnvVarSet:    true,
			clusterVersion: &semver.Version{Major: 1, Minor: 0, Patch: 0},
			expectedOutput: nil,
		},
	}

	for _, testCase := range testCases {
		// set environment as required
		if testCase.isEnvVarSet {
			require.Nil(t, os.Setenv(SystemConstraintEnvVarName, "true"))
		} else {
			require.Nil(t, os.Unsetenv(SystemConstraintEnvVarName))
		}

		// create clusterProperties if necessary
		if testCase.clusterVersion != nil {
			clusterPropertiesBuilder = func() clusterProperties {
				return &fakeProperties{
					version: testCase.clusterVersion,
				}
			}
		}

		// execute test in func so defer can handle panic conditions
		func() {
			defer func() {
				// reset clusterPropertiesBuilder
				clusterPropertiesBuilder = oldClusterPropertiesBuilder

				// check for expected panic/error behavior
				err := recover()
				if err == nil && testCase.expectPanic {
					t.Errorf("The code did not panic")
				} else if err != nil && !testCase.expectPanic {
					t.Errorf("Unexpected panic: %s", err)
				}
			}()

			err := openShiftStepResolverInit(testCase.stepResolver)
			require.Equal(t, testCase.expectedOutput, err)

			// if err is not nil (and there's no panic)
			// ensure the satResolver's systemConstraintsProvider is set
			if err != nil {
				require.NotNil(t, testCase.stepResolver.satResolver.systemConstraintsProvider)
			}
		}()
	}
}

type fakeProperties struct {
	version *semver.Version
}

func (f *fakeProperties) Version() (*semver.Version, error) {
	return f.version, nil
}

func TestOpenShiftConstraintsProvider_Constraints(t *testing.T) {
	testCases := []struct {
		title                 string
		clusterVersion        string
		maxOpenShiftVersion   string
		expectedConstraints   []solver.Constraint
		expectedError         error
		omitVersionProperty   bool
		omitClusterProperties bool
	}{
		{
			title:               "cluster version == maxOpenShiftVersion -> no constraints",
			clusterVersion:      "1.0.0",
			maxOpenShiftVersion: "1.0",
			expectedConstraints: nil,
			expectedError:       nil,
		},
		{
			title:               "cluster version < maxOpenShiftVersion -> no constraints",
			clusterVersion:      "1.0",
			maxOpenShiftVersion: "2.0.0",
			expectedConstraints: nil,
			expectedError:       nil,
		},
		{
			title:               "cluster version > maxOpenShiftVersion -> prohibited constraint",
			clusterVersion:      "1.0",
			maxOpenShiftVersion: "0.9.0",
			expectedConstraints: []solver.Constraint{
				PrettyConstraint(solver.Prohibited(), "bundle incompatible with openshift cluster, \"olm.maxOpenShiftVersion\" < cluster version: (0.9 < 1.0)"),
			},
			expectedError: nil,
		},
		{
			title:               "bad maxOpenShiftVersion -> prohibited constraint",
			clusterVersion:      "1.0",
			maxOpenShiftVersion: "bad version",
			expectedConstraints: []solver.Constraint{
				PrettyConstraint(
					solver.Prohibited(),
					"invalid \"olm.maxOpenShiftVersion\" property: failed to parse \"bad version\" as semver: Invalid character(s) found in major number \"0bad version\""),
			},
			expectedError: nil,
		},
		{
			title:               "maxOpenShiftVersion undefined -> no constraint",
			clusterVersion:      "1.0",
			omitVersionProperty: true,
			expectedConstraints: nil,
			expectedError:       nil,
		},
		{
			title:                 "properties undefined -> error",
			omitClusterProperties: true,
			expectedConstraints:   nil,
			expectedError:         errors.New("nil clusterProperties"),
		},
		{
			title:               "prohibit package if maxOpenShiftVersion includes patch",
			clusterVersion:      "1.0",
			maxOpenShiftVersion: "2.0.99",
			expectedConstraints: []solver.Constraint{
				PrettyConstraint(
					solver.Prohibited(),
					"invalid \"olm.maxOpenShiftVersion\" property: property \"olm.maxOpenShiftVersion\" must specify only <major>.<minor> version, got invalid value 2.0.99"),
			},
			expectedError: nil,
		},
		{
			title:               "prohibit package if maxOpenShiftVersion includes preview",
			clusterVersion:      "1.0",
			maxOpenShiftVersion: "2.0-preview",
			expectedConstraints: []solver.Constraint{
				PrettyConstraint(
					solver.Prohibited(),
					"invalid \"olm.maxOpenShiftVersion\" property: failed to parse \"2.0-preview\" as semver: Short version cannot contain PreRelease/Build meta data"),
			},
			expectedError: nil,
		},
		{
			title:               "prohibit package if maxOpenShiftVersion includes build metadata",
			clusterVersion:      "1.0",
			maxOpenShiftVersion: "2.0+123.github",
			expectedConstraints: []solver.Constraint{
				PrettyConstraint(
					solver.Prohibited(),
					"invalid \"olm.maxOpenShiftVersion\" property: failed to parse \"2.0+123.github\" as semver: Invalid character(s) found in minor number \"0+123\""),
			},
			expectedError: nil,
		},
	}

	for _, testCase := range testCases {
		var props clusterProperties = nil
		var entry = &cache.Entry{
			Properties: []*api.Property{},
		}

		if !(testCase.omitClusterProperties) {
			version, err := semver.ParseTolerant(testCase.clusterVersion)
			require.Nil(t, err)
			props = &fakeProperties{
				version: &version,
			}
		}

		if !(testCase.omitVersionProperty) {
			entry.Properties = append(entry.Properties,
				&api.Property{
					Type:  openshift.MaxOpenShiftVersionProperty,
					Value: testCase.maxOpenShiftVersion,
				})
		}

		provider := newOpenShiftSystemConstraintsProvider(props)
		actualConstraints, actualError := provider.Constraints(entry)
		require.Equal(t, actualConstraints, testCase.expectedConstraints)
		require.Equal(t, actualError, testCase.expectedError)
	}
}

func TestOpenShiftClusterProperties_Version(t *testing.T) {
	testCases := []struct {
		title           string
		client          configv1client.ClusterVersionsGetter
		expectedErr     error
		expectedVersion *semver.Version
	}{
		{
			title:           "raises error if client is undefined",
			client:          nil,
			expectedVersion: nil,
			expectedErr:     errors.New("nil client"),
		},
		{
			title: "raises if client failed to retrieve version",
			client: fakeClusterVersionClient{
				clusterVersion:       nil,
				expectedResourceName: "version",
				expectedGetOpts:      metav1.GetOptions{},
				err:                  errors.New("some error"),
				t:                    t,
			},
			expectedErr: errors.New("failed to get cluster version: some error"),
		},
		{
			title: "raises error if client failed to retrieve version",
			client: fakeClusterVersionClient{
				clusterVersion:       nil,
				expectedResourceName: "version",
				expectedGetOpts:      metav1.GetOptions{},
				err:                  errors.New("some error"),
				t:                    t,
			},
			expectedErr: errors.New("failed to get cluster version: some error"),
		},
		{
			title: "raises error if cluster version is nil",
			client: fakeClusterVersionClient{
				clusterVersion:       nil,
				expectedResourceName: "version",
				expectedGetOpts:      metav1.GetOptions{},
				err:                  nil,
				t:                    t,
			},
			expectedErr: errors.New("incorrect client behavior observed"),
		},
		{
			title: "raises error if desired version is not set",
			client: fakeClusterVersionClient{
				clusterVersion: &configv1.ClusterVersion{
					Status: configv1.ClusterVersionStatus{
						Desired: configv1.Update{
							Version: "",
						},
					},
				},
				expectedResourceName: "version",
				expectedGetOpts:      metav1.GetOptions{},
				err:                  nil,
				t:                    t,
			},
			expectedErr: errors.New("desired release missing from resource"),
		},
		{
			title: "raises error if desired version is cannot be parsed",
			client: fakeClusterVersionClient{
				clusterVersion: &configv1.ClusterVersion{
					Status: configv1.ClusterVersionStatus{
						Desired: configv1.Update{
							Version: "bad version format",
						},
					},
				},
				expectedResourceName: "version",
				expectedGetOpts:      metav1.GetOptions{},
				err:                  nil,
				t:                    t,
			},
			expectedErr: errors.New("resource has invalid desired release: Invalid character(s) found in major number \"0bad version format\""),
		},
		{
			title: "happy path",
			client: fakeClusterVersionClient{
				clusterVersion: &configv1.ClusterVersion{
					Status: configv1.ClusterVersionStatus{
						Desired: configv1.Update{
							Version: "1.0.99",
						},
					},
				},
				expectedResourceName: "version",
				expectedGetOpts:      metav1.GetOptions{},
				err:                  nil,
				t:                    t,
			},
			expectedVersion: &semver.Version{Major: 1, Minor: 0, Patch: 99},
		},
	}

	for _, testCase := range testCases {
		props := newOpenShiftClusterProperties(testCase.client)
		version, err := props.Version()
		require.Equal(t, testCase.expectedVersion, version)

		if testCase.expectedErr != nil && err != nil {
			require.EqualError(t, testCase.expectedErr, err.Error())
		} else {
			require.Equal(t, testCase.expectedErr, err)
		}
	}
}

type fakeClusterVersionClient struct {
	clusterVersion       *configv1.ClusterVersion
	err                  error
	t                    *testing.T
	expectedResourceName string
	expectedGetOpts      metav1.GetOptions
}

func (f fakeClusterVersionClient) ClusterVersions() configv1client.ClusterVersionInterface {
	return &f
}

func (f fakeClusterVersionClient) Get(ctx context.Context, name string, opts metav1.GetOptions) (*configv1.ClusterVersion, error) {
	require.Equal(f.t, f.expectedResourceName, name)
	require.Equal(f.t, f.expectedGetOpts, opts)
	return f.clusterVersion, f.err
}

func (f fakeClusterVersionClient) Create(ctx context.Context, clusterVersion *configv1.ClusterVersion, opts metav1.CreateOptions) (*configv1.ClusterVersion, error) {
	panic("implement me")
}

func (f fakeClusterVersionClient) Update(ctx context.Context, clusterVersion *configv1.ClusterVersion, opts metav1.UpdateOptions) (*configv1.ClusterVersion, error) {
	panic("implement me")
}

func (f fakeClusterVersionClient) UpdateStatus(ctx context.Context, clusterVersion *configv1.ClusterVersion, opts metav1.UpdateOptions) (*configv1.ClusterVersion, error) {
	panic("implement me")
}

func (f fakeClusterVersionClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	panic("implement me")
}

func (f fakeClusterVersionClient) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	panic("implement me")
}

func (f fakeClusterVersionClient) List(ctx context.Context, opts metav1.ListOptions) (*configv1.ClusterVersionList, error) {
	panic("implement me")
}

func (f fakeClusterVersionClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	panic("implement me")
}

func (f fakeClusterVersionClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *configv1.ClusterVersion, err error) {
	panic("implement me")
}
