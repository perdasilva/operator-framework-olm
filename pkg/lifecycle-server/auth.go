package server

import (
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/apis/apiserver"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/authenticatorfactory"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	authenticationv1 "k8s.io/client-go/kubernetes/typed/authentication/v1"
	authorizationv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"

	"github.com/go-logr/logr"
)

const (
	apiGroup = "lifecycle.olm.openshift.io"
	resource = "lifecycles"
)

// NewAuthFilter creates HTTP middleware that authenticates requests via
// TokenReview and authorizes them via SubjectAccessReview using resource-based
// RBAC. Callers must have a ClusterRole granting "get" on the "lifecycles"
// resource in the "lifecycle.olm.openshift.io" API group.
func NewAuthFilter(cfg *rest.Config, httpClient *http.Client, log logr.Logger) (func(http.Handler) http.Handler, error) {
	authenticationClient, err := authenticationv1.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create authentication client: %w", err)
	}
	authorizationClient, err := authorizationv1.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create authorization client: %w", err)
	}

	retryBackoff := &wait.Backoff{
		Duration: 500 * time.Millisecond,
		Factor:   1.5,
		Jitter:   0.2,
		Steps:    5,
	}

	authn, _, err := authenticatorfactory.DelegatingAuthenticatorConfig{
		Anonymous:                &apiserver.AnonymousAuthConfig{Enabled: false},
		CacheTTL:                 1 * time.Minute,
		TokenAccessReviewClient:  authenticationClient,
		TokenAccessReviewTimeout: 10 * time.Second,
		WebhookRetryBackoff:      retryBackoff,
	}.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create authenticator: %w", err)
	}

	authz, err := authorizerfactory.DelegatingAuthorizerConfig{
		SubjectAccessReviewClient: authorizationClient,
		AllowCacheTTL:             5 * time.Minute,
		DenyCacheTTL:              30 * time.Second,
		WebhookRetryBackoff:       retryBackoff,
	}.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create authorizer: %w", err)
	}

	return newAuthMiddleware(authn, authz, log), nil
}

func newAuthMiddleware(authn authenticator.Request, authz authorizer.Authorizer, log logr.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			res, ok, err := authn.AuthenticateRequest(req)
			if err != nil {
				log.Error(err, "authentication failed")
				http.Error(w, "Authentication failed", http.StatusInternalServerError)
				return
			}
			if !ok {
				log.V(4).Info("authentication failed")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			decision, reason, err := authz.Authorize(req.Context(), authorizer.AttributesRecord{
				User:            res.User,
				Verb:            "get",
				APIGroup:        apiGroup,
				Resource:        resource,
				ResourceRequest: true,
			})
			if err != nil {
				log.Error(err, "authorization failed", "user", res.User.GetName())
				http.Error(w, "Authorization failed", http.StatusInternalServerError)
				return
			}
			if decision != authorizer.DecisionAllow {
				log.V(4).Info("authorization denied", "user", res.User.GetName(), "reason", reason)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, req)
		})
	}
}
