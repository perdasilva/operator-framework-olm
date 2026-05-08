package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

type fakeAuthenticator struct {
	resp *authenticator.Response
	ok   bool
	err  error
}

func (f *fakeAuthenticator) AuthenticateRequest(_ *http.Request) (*authenticator.Response, bool, error) {
	return f.resp, f.ok, f.err
}

type fakeAuthorizer struct {
	decision authorizer.Decision
	reason   string
	err      error
	attrs    authorizer.Attributes
}

func (f *fakeAuthorizer) Authorize(_ context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	f.attrs = a
	return f.decision, f.reason, f.err
}

func TestAuthMiddleware_Unauthenticated(t *testing.T) {
	authn := &fakeAuthenticator{ok: false}
	authz := &fakeAuthorizer{decision: authorizer.DecisionAllow}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		handlerCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.False(t, handlerCalled, "inner handler should not have been called")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_AuthenticationError(t *testing.T) {
	authn := &fakeAuthenticator{err: http.ErrAbortHandler}
	authz := &fakeAuthorizer{decision: authorizer.DecisionAllow}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		handlerCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.False(t, handlerCalled, "inner handler should not have been called")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAuthMiddleware_Forbidden(t *testing.T) {
	authn := &fakeAuthenticator{
		resp: &authenticator.Response{User: &user.DefaultInfo{Name: "test-user"}},
		ok:   true,
	}
	authz := &fakeAuthorizer{decision: authorizer.DecisionDeny, reason: "no binding"}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		handlerCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.False(t, handlerCalled, "inner handler should not have been called")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), "no binding", "response must not leak authorization reason")
}

func TestAuthMiddleware_NoOpinion(t *testing.T) {
	authn := &fakeAuthenticator{
		resp: &authenticator.Response{User: &user.DefaultInfo{Name: "test-user"}},
		ok:   true,
	}
	authz := &fakeAuthorizer{decision: authorizer.DecisionNoOpinion, reason: "no RBAC policy matched"}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		handlerCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.False(t, handlerCalled, "inner handler should not have been called")
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAuthMiddleware_Allowed(t *testing.T) {
	authn := &fakeAuthenticator{
		resp: &authenticator.Response{User: &user.DefaultInfo{Name: "test-user"}},
		ok:   true,
	}
	authz := &fakeAuthorizer{decision: authorizer.DecisionAllow}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.True(t, handlerCalled, "inner handler should have been called")
	require.Equal(t, http.StatusOK, rec.Code)

}

func TestAuthMiddleware_AttributesRecord(t *testing.T) {
	authn := &fakeAuthenticator{
		resp: &authenticator.Response{User: &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}},
		ok:   true,
	}
	authz := &fakeAuthorizer{decision: authorizer.DecisionAllow}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		handlerCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.True(t, handlerCalled, "inner handler should have been called")
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, authz.attrs.IsResourceRequest())
	require.Equal(t, "get", authz.attrs.GetVerb())
	require.Equal(t, apiGroup, authz.attrs.GetAPIGroup())
	require.Equal(t, resource, authz.attrs.GetResource())
	require.Equal(t, "test-user", authz.attrs.GetUser().GetName())
}

func TestAuthMiddleware_AuthorizationError(t *testing.T) {
	authn := &fakeAuthenticator{
		resp: &authenticator.Response{User: &user.DefaultInfo{Name: "test-user"}},
		ok:   true,
	}
	authz := &fakeAuthorizer{err: http.ErrAbortHandler}
	middleware := newAuthMiddleware(authn, authz, logr.Discard())

	var handlerCalled bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		handlerCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.False(t, handlerCalled, "inner handler should not have been called")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
