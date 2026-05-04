package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

// staticLookup returns a CacheLookup that always returns the given data.
func staticLookup(data LifecycleIndex) CacheLookup {
	return func(namespace, name string) (LifecycleIndex, bool) {
		if data == nil {
			return nil, false
		}
		return data, true
	}
}

// catalogLookup returns a CacheLookup backed by a map of namespace/name -> LifecycleIndex.
func catalogLookup(catalogs map[string]LifecycleIndex) CacheLookup {
	return func(namespace, name string) (LifecycleIndex, bool) {
		key := namespace + "/" + name
		data, found := catalogs[key]
		return data, found
	}
}

func TestNewHandler(t *testing.T) {
	testBlob := json.RawMessage(`{"eol":"2025-12-31","status":"active"}`)

	tt := []struct {
		name           string
		lookup         CacheLookup
		method         string
		path           string
		expectedStatus int
		expectedBody   string
		expectedCT     string
	}{
		{
			name: "valid version, namespace, name, and package returns 200",
			lookup: catalogLookup(map[string]LifecycleIndex{
				"test-ns/my-catalog": {
					"v1alpha1": {"my-operator": testBlob},
				},
			}),
			method:         http.MethodGet,
			path:           "/api/v1alpha1/test-ns/my-catalog/lifecycles/my-operator",
			expectedStatus: http.StatusOK,
			expectedBody:   `{"eol":"2025-12-31","status":"active"}`,
			expectedCT:     "application/json",
		},
		{
			name: "catalog not cached returns 404",
			lookup: func(namespace, name string) (LifecycleIndex, bool) {
				return nil, false
			},
			method:         http.MethodGet,
			path:           "/api/v1alpha1/test-ns/my-catalog/lifecycles/my-operator",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "negative cache (nil data) returns 404",
			lookup: catalogLookup(map[string]LifecycleIndex{
				"test-ns/my-catalog": nil,
			}),
			method:         http.MethodGet,
			path:           "/api/v1alpha1/test-ns/my-catalog/lifecycles/my-operator",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "unknown version returns 404",
			lookup: catalogLookup(map[string]LifecycleIndex{
				"test-ns/my-catalog": {
					"v1alpha1": {"my-operator": testBlob},
				},
			}),
			method:         http.MethodGet,
			path:           "/api/v2/test-ns/my-catalog/lifecycles/my-operator",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "unknown package returns 404",
			lookup: catalogLookup(map[string]LifecycleIndex{
				"test-ns/my-catalog": {
					"v1alpha1": {"my-operator": testBlob},
				},
			}),
			method:         http.MethodGet,
			path:           "/api/v1alpha1/test-ns/my-catalog/lifecycles/other-operator",
			expectedStatus: http.StatusNotFound,
		},
		{
			name: "POST method not allowed",
			lookup: catalogLookup(map[string]LifecycleIndex{
				"test-ns/my-catalog": {
					"v1alpha1": {"my-operator": testBlob},
				},
			}),
			method:         http.MethodPost,
			path:           "/api/v1alpha1/test-ns/my-catalog/lifecycles/my-operator",
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewHandler(tc.lookup, logr.Discard())

			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()
			require.Equal(t, tc.expectedStatus, resp.StatusCode, "unexpected status code")

			if tc.expectedBody != "" {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tc.expectedBody, string(body))
			}

			if tc.expectedCT != "" {
				require.Equal(t, tc.expectedCT, resp.Header.Get("Content-Type"))
			}
		})
	}
}

func TestNewHandler_RawBlobReturnedByteForByte(t *testing.T) {
	originalBlob := json.RawMessage(`{"keys":"in-specific-order","numbers":42,"nested":{"a":1}}`)

	lookup := catalogLookup(map[string]LifecycleIndex{
		"ns/catalog": {
			"v1alpha1": {"test-pkg": originalBlob},
		},
	})

	handler := NewHandler(lookup, logr.Discard())
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/ns/catalog/lifecycles/test-pkg", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, string(originalBlob), string(body))
}

func TestNewHandler_ConcurrentRequests(t *testing.T) {
	testBlob := json.RawMessage(`{"status":"active","eol":"2025-12-31"}`)
	lookup := catalogLookup(map[string]LifecycleIndex{
		"ns/catalog": {
			"v1alpha1": {"my-operator": testBlob},
		},
	})
	handler := NewHandler(lookup, logr.Discard())

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errCh := make(chan error, goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/ns/catalog/lifecycles/my-operator", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("expected status 200, got %d", resp.StatusCode)
				return
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errCh <- fmt.Errorf("failed to read body: %w", err)
				return
			}
			if string(body) != string(testBlob) {
				errCh <- fmt.Errorf("body mismatch: got %q, want %q", string(body), string(testBlob))
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

func TestNewHealthHandler(t *testing.T) {
	handler := NewHealthHandler()

	tt := []struct {
		name           string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "healthz always returns 200",
			path:           "/healthz",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "readyz always returns 200",
			path:           "/readyz",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()
			require.Equal(t, tc.expectedStatus, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), tc.expectedBody)
		})
	}
}

func TestNewHandler_MultipleVersions(t *testing.T) {
	blobV1Alpha1 := json.RawMessage(`{"version":"v1alpha1","status":"active"}`)
	blobV1Beta1 := json.RawMessage(`{"version":"v1beta1","status":"deprecated"}`)

	lookup := catalogLookup(map[string]LifecycleIndex{
		"ns/catalog": {
			"v1alpha1": {"my-operator": blobV1Alpha1},
			"v1beta1":  {"my-operator": blobV1Beta1},
		},
	})
	handler := NewHandler(lookup, logr.Discard())

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/ns/catalog/lifecycles/my-operator", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, string(blobV1Alpha1), string(body))

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1beta1/ns/catalog/lifecycles/my-operator", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	resp2 := rec2.Result()
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body2, _ := io.ReadAll(resp2.Body)
	require.Equal(t, string(blobV1Beta1), string(body2))
}
