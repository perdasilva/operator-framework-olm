package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	tlsutil "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/operator-framework-olm/pkg/leaderelection"
	controllers "github.com/openshift/operator-framework-olm/pkg/lifecycle-controller"
	server "github.com/openshift/operator-framework-olm/pkg/lifecycle-server"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsfilters "sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	defaultMetricsAddr     = ":8443"
	defaultAPIAddr         = ":9443"
	defaultHealthCheckAddr = ":8081"
	leaderElectionID       = "lifecycle-controller-lock"
	shutdownTimeout        = 10 * time.Second
	readHeaderTimeout      = 5 * time.Second
	readTimeout            = 10 * time.Second
	writeTimeout           = 30 * time.Second
	idleTimeout            = 120 * time.Second
)

var (
	disableLeaderElection      bool
	healthCheckAddr            string
	metricsAddr                string
	apiAddr                    string
	catalogSourceLabelSelector string
	catalogSourceFieldSelector string
	tlsCertFile                string
	tlsKeyFile                 string
)

func newStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "start",
		Short:        "Start the Lifecycle Controller",
		SilenceUsage: true,
		RunE:         run,
	}

	cmd.Flags().StringVar(&healthCheckAddr, "health", defaultHealthCheckAddr, "health check address")
	cmd.Flags().StringVar(&metricsAddr, "metrics", defaultMetricsAddr, "metrics address")
	cmd.Flags().StringVar(&apiAddr, "api", defaultAPIAddr, "lifecycle API address")
	cmd.Flags().BoolVar(&disableLeaderElection, "disable-leader-election", false, "disable leader election")
	cmd.Flags().StringVar(&catalogSourceLabelSelector, "catalog-source-label-selector", "", "label selector for catalog sources to manage (empty means all)")
	cmd.Flags().StringVar(&catalogSourceFieldSelector, "catalog-source-field-selector", "", "field selector for catalog sources to manage (empty means all)")
	cmd.Flags().StringVar(&tlsCertFile, "tls-cert", "", "path to TLS certificate file")
	cmd.Flags().StringVar(&tlsKeyFile, "tls-key", "", "path to TLS key file")
	_ = cmd.MarkFlagRequired("tls-cert")
	_ = cmd.MarkFlagRequired("tls-key")
	return cmd
}

func run(_ *cobra.Command, _ []string) error {
	ctx := ctrl.SetupSignalHandler()
	ctrl.SetLogger(klog.NewKlogr())
	setupLog := ctrl.Log.WithName("setup")

	cfg, err := loadStartConfig(ctx)
	if err != nil {
		return fmt.Errorf("unable to load startup configuration: %w", err)
	}
	logConfig(cfg, setupLog)

	lifecycleCache := controllers.NewLifecycleCache()

	mgr, err := setupManager(cfg)
	if err != nil {
		return fmt.Errorf("failed to setup manager instance: %w", err)
	}

	if err := setupLifecycleController(mgr, cfg, lifecycleCache); err != nil {
		return fmt.Errorf("unable to setup lifecycle controller: %w", err)
	}

	apiServer, err := setupAPIServer(cfg, lifecycleCache)
	if err != nil {
		return fmt.Errorf("failed to setup API server: %w", err)
	}

	// Start API server in background
	errChan := make(chan error, 1)
	go func() {
		setupLog.Info("starting lifecycle API server", "addr", apiAddr)
		if err := apiServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- fmt.Errorf("lifecycle API server error: %w", err)
		}
	}()

	// Start manager (blocks until context is cancelled)
	setupLog.Info("starting manager")
	mgrErr := mgr.Start(ctx)

	// Graceful shutdown of API server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		setupLog.Error(err, "failed to gracefully shutdown API server")
	}

	// Check for API server error
	select {
	case err := <-errChan:
		if mgrErr != nil {
			return mgrErr
		}
		return err
	default:
	}

	return mgrErr
}

type startConfig struct {
	Namespace string
	Version   string

	CatalogSourceFieldSelector fields.Selector
	CatalogSourceLabelSelector labels.Selector
	RESTConfig                 *rest.Config
	Scheme                     *runtime.Scheme

	LeaderElection configv1.LeaderElection

	InitialTLSProfileSpec   configv1.TLSProfileSpec
	TLSConfigProvider       *controllers.TLSConfigProvider
	EnableTLSProfileWatcher bool
}

func loadStartConfig(ctx context.Context) (*startConfig, error) {
	cfg := &startConfig{
		Namespace: os.Getenv("NAMESPACE"),
		Version:   cmp.Or(os.Getenv("RELEASE_VERSION"), "unknown"),
	}
	if cfg.Namespace == "" && !disableLeaderElection {
		return nil, fmt.Errorf("NAMESPACE environment variable is required when leader election is enabled")
	}

	getCertificate := func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert, err := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
		if err != nil {
			return nil, err
		}
		return &cert, nil
	}
	if _, err := getCertificate(nil); err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate/key: %w", err)
	}

	var err error
	cfg.CatalogSourceFieldSelector, err = fields.ParseSelector(catalogSourceFieldSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse catalog source field selector %q: %w", catalogSourceFieldSelector, err)
	}
	cfg.CatalogSourceLabelSelector, err = labels.Parse(catalogSourceLabelSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse catalog source label selector %q: %w", catalogSourceLabelSelector, err)
	}
	cfg.RESTConfig, err = ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get rest config: %w", err)
	}
	cfg.Scheme = setupScheme()
	cfg.LeaderElection = leaderelection.GetLeaderElectionConfig(ctrl.Log.WithName("leaderelection"), cfg.RESTConfig, !disableLeaderElection)

	cfg.InitialTLSProfileSpec, cfg.EnableTLSProfileWatcher, err = getInitialTLSProfile(ctx, cfg.RESTConfig, cfg.Scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to get initial TLS security profile: %w", err)
	}
	cfg.TLSConfigProvider = controllers.NewTLSConfigProvider(getCertificate, cfg.InitialTLSProfileSpec)
	return cfg, nil
}

func logConfig(cfg *startConfig, log logr.Logger) {
	log.Info("starting lifecycle-controller", "version", cfg.Version)
	if !cfg.CatalogSourceLabelSelector.Empty() {
		log.Info("config", "catalogSourceLabelSelector", cfg.CatalogSourceLabelSelector.String())
	}
	if !cfg.CatalogSourceFieldSelector.Empty() {
		log.Info("config", "catalogSourceFieldSelector", cfg.CatalogSourceFieldSelector.String())
	}
	tlsProfile, unsupportedCiphers := cfg.TLSConfigProvider.Get()
	log.Info("config", "tlsMinVersion", crypto.TLSVersionToNameOrDie(tlsProfile.MinVersion))
	log.Info("config", "tlsCipherSuites", crypto.CipherSuitesToNamesOrDie(tlsProfile.CipherSuites))
	if len(unsupportedCiphers) > 0 {
		log.Error(errors.New("ignored config"), "unsupported TLS cipher suites", "tlsCipherSuites", unsupportedCiphers)
	}
}

func getInitialTLSProfile(ctx context.Context, restConfig *rest.Config, sch *runtime.Scheme) (configv1.TLSProfileSpec, bool, error) {
	cl, err := client.New(restConfig, client.Options{Scheme: sch})
	if err != nil {
		return configv1.TLSProfileSpec{}, false, fmt.Errorf("failed to create client: %w", err)
	}
	initialTLSProfileSpec, err := tlsutil.FetchAPIServerTLSProfile(ctx, cl)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return *configv1.TLSProfiles[crypto.DefaultTLSProfileType], false, nil
		}
		return configv1.TLSProfileSpec{}, false, fmt.Errorf("failed to fetch APIServer TLS profile: %w", err)
	}
	return initialTLSProfileSpec, true, nil
}

func setupManager(cfg *startConfig) (manager.Manager, error) {
	mgr, err := ctrl.NewManager(cfg.RESTConfig, manager.Options{
		Scheme: cfg.Scheme,
		Metrics: metricsserver.Options{
			BindAddress:    metricsAddr,
			SecureServing:  true,
			FilterProvider: metricsfilters.WithAuthenticationAndAuthorization,
			TLSOpts: []func(*tls.Config){func(tlsConfig *tls.Config) {
				tlsConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
					tlsCfg, _ := cfg.TLSConfigProvider.Get()
					return tlsCfg, nil
				}
			}},
		},
		LeaderElection:                !cfg.LeaderElection.Disable,
		LeaderElectionNamespace:       cfg.Namespace,
		LeaderElectionID:              leaderElectionID,
		LeaseDuration:                 &cfg.LeaderElection.LeaseDuration.Duration,
		RenewDeadline:                 &cfg.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:                   &cfg.LeaderElection.RetryPeriod.Duration,
		HealthProbeBindAddress:        healthCheckAddr,
		LeaderElectionReleaseOnCancel: true,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&operatorsv1alpha1.CatalogSource{}: {},
				&corev1.Pod{}: {
					Label: controllers.CatalogPodLabelSelector(),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create manager: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", func(req *http.Request) error {
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to configure health check handler: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", func(req *http.Request) error {
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to configure readiness check handler: %w", err)
	}
	return mgr, nil
}

func setupAPIServer(cfg *startConfig, lifecycleCache *controllers.LifecycleCache) (*http.Server, error) {
	log := ctrl.Log.WithName("api-server")

	lookup := func(namespace, name string) (server.LifecycleIndex, bool) {
		entry, found := lifecycleCache.Get(namespace, name)
		if !found {
			return nil, false
		}
		return entry.Data, true
	}

	handler := server.NewHandler(lookup, log)

	tlsCfg, _ := cfg.TLSConfigProvider.Get()
	apiTLSConfig := tlsCfg.Clone()
	apiTLSConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		cfg, _ := cfg.TLSConfigProvider.Get()
		return cfg, nil
	}

	return &http.Server{
		Addr:              apiAddr,
		Handler:           handler,
		TLSConfig:         apiTLSConfig,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext: func(l net.Listener) context.Context {
			return context.Background()
		},
	}, nil
}

func setupLifecycleController(mgr manager.Manager, cfg *startConfig, lifecycleCache *controllers.LifecycleCache) error {
	log := ctrl.Log.WithName("controllers").WithName("lifecycle")

	// Use a direct (uncached) client for secret reads to avoid caching all
	// cluster secrets and to keep RBAC minimal (get only, no list/watch).
	directClient, err := client.New(cfg.RESTConfig, client.Options{Scheme: cfg.Scheme})
	if err != nil {
		return fmt.Errorf("failed to create direct client for puller: %w", err)
	}
	puller := controllers.NewImagePuller(directClient, log)

	reconciler := &controllers.LifecycleReconciler{
		Client:                     mgr.GetClient(),
		Log:                        log,
		Cache:                      lifecycleCache,
		Puller:                     puller,
		CatalogSourceLabelSelector: cfg.CatalogSourceLabelSelector,
		CatalogSourceFieldSelector: cfg.CatalogSourceFieldSelector,
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to setup lifecycle controller: %w", err)
	}
	return nil
}
