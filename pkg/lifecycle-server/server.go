/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"net/http"

	"github.com/go-logr/logr"
)

// CacheLookup is a function that retrieves cached lifecycle data for a catalog source.
// Returns the LifecycleIndex and true if the entry exists (even if empty/negative cache),
// or nil and false if the catalog source has not been cached.
type CacheLookup func(namespace, name string) (LifecycleIndex, bool)

// NewHealthHandler creates an HTTP handler for health and readiness probes.
// The /healthz endpoint always returns 200. The /readyz endpoint always returns 200
// (the controller is healthy regardless of cached data).
func NewHealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// NewHandler creates an HTTP handler for the lifecycle API.
// It serves GET /api/{version}/{namespace}/{name}/lifecycles/{package}, returning the raw JSON
// blob for the given catalog source, version, and package, or 404 if not found.
func NewHandler(lookup CacheLookup, log logr.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/{version}/{namespace}/{name}/lifecycles/{package}", func(w http.ResponseWriter, r *http.Request) {
		version := r.PathValue("version")
		namespace := r.PathValue("namespace")
		name := r.PathValue("name")
		pkg := r.PathValue("package")

		data, found := lookup(namespace, name)
		if !found {
			log.V(1).Info("catalog source not cached", "namespace", namespace, "name", name)
			http.NotFound(w, r)
			return
		}

		// Negative cache: image was pulled but had no lifecycle data
		if len(data) == 0 {
			log.V(1).Info("catalog source has no lifecycle data", "namespace", namespace, "name", name)
			http.NotFound(w, r)
			return
		}

		versionData, ok := data[version]
		if !ok {
			log.V(1).Info("version not found", "namespace", namespace, "name", name, "version", version)
			http.NotFound(w, r)
			return
		}

		rawJSON, ok := versionData[pkg]
		if !ok {
			log.V(1).Info("package not found", "namespace", namespace, "name", name, "version", version, "package", pkg)
			http.NotFound(w, r)
			return
		}

		log.V(1).Info("returning lifecycle data", "namespace", namespace, "name", name, "version", version, "package", pkg)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(rawJSON); err != nil {
			log.V(1).Error(err, "failed to write response")
		}
	})

	return mux
}
