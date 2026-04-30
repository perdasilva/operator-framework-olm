package controllers

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/operator-framework/operator-registry/alpha/declcfg"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	server "github.com/openshift/operator-framework-olm/pkg/lifecycle-server"
)

const (
	// fbcRootDir is the well-known path inside catalog images where FBC configs live.
	fbcRootDir = "configs"
)

// ImagePuller pulls OCI images and extracts lifecycle data from FBC content.
type ImagePuller struct {
	client client.Reader
	log    logr.Logger
}

func NewImagePuller(c client.Reader, log logr.Logger) *ImagePuller {
	return &ImagePuller{client: c, log: log}
}

// PullAndExtractLifecycleData pulls the image at imageRef, extracts FBC content
// from the /configs directory, and returns lifecycle data indexed by version and package.
// pullSecretNames are the names of Kubernetes Secrets in the given namespace to use for auth.
func (p *ImagePuller) PullAndExtractLifecycleData(ctx context.Context, imageRef string, pullSecretNames []string, namespace string) (server.LifecycleIndex, error) {
	opts := []crane.Option{crane.WithContext(ctx)}

	keychain, err := p.buildKeychain(ctx, pullSecretNames, namespace)
	if err != nil {
		return nil, fmt.Errorf("building keychain: %w", err)
	}
	if keychain != nil {
		opts = append(opts, crane.WithAuthFromKeychain(keychain))
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference %q: %w", imageRef, err)
	}

	p.log.V(1).Info("pulling image", "ref", ref.String())
	img, err := crane.Pull(ref.String(), opts...)
	if err != nil {
		return nil, fmt.Errorf("pulling image %q: %w", imageRef, err)
	}

	return p.extractLifecycleData(img)
}

// extractLifecycleData reads all layers from the image, finds files under /configs/,
// and parses them for lifecycle schema blobs.
func (p *ImagePuller) extractLifecycleData(img v1.Image) (server.LifecycleIndex, error) {
	result := make(server.LifecycleIndex)

	reader := mutate.Extract(img)
	defer reader.Close()

	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading image layer: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Normalize the path: strip leading "/" or "./"
		cleanPath := filepath.Clean(header.Name)
		cleanPath = strings.TrimPrefix(cleanPath, "/")

		if !isUnderConfigsDir(cleanPath) {
			continue
		}

		ext := filepath.Ext(cleanPath)
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}

		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading file %q from image: %w", cleanPath, err)
		}

		if err := p.parseLifecycleBlobs(content, result); err != nil {
			return nil, fmt.Errorf("parsing lifecycle data from %q: %w", cleanPath, err)
		}
	}

	p.log.V(1).Info("extracted lifecycle data", "blobs", result.CountBlobs(), "packages", result.CountPackages(), "versions", result.ListVersions())
	return result, nil
}

// parseLifecycleBlobs parses FBC meta entries from content and adds matching lifecycle blobs to result.
func (p *ImagePuller) parseLifecycleBlobs(content []byte, result server.LifecycleIndex) error {
	return declcfg.WalkMetasReader(bytes.NewReader(content), func(meta *declcfg.Meta, err error) error {
		if err != nil {
			p.log.V(1).Info("skipping FBC entry due to error", "error", err)
			return nil
		}
		if meta == nil {
			return nil
		}

		matches := server.SchemaVersionRegex.FindStringSubmatch(meta.Schema)
		if matches == nil {
			return nil
		}
		schemaVersion := matches[1]

		if meta.Package == "" {
			return nil
		}

		if result[schemaVersion] == nil {
			result[schemaVersion] = make(map[string]json.RawMessage)
		}
		if _, exists := result[schemaVersion][meta.Package]; exists {
			return fmt.Errorf("duplicate lifecycle blob for version %q package %q", schemaVersion, meta.Package)
		}
		result[schemaVersion][meta.Package] = meta.Blob
		return nil
	})
}

// isUnderConfigsDir checks whether a path is under the well-known configs directory.
func isUnderConfigsDir(path string) bool {
	return path == fbcRootDir || strings.HasPrefix(path, fbcRootDir+"/")
}

// buildKeychain builds an authn.Keychain from the named Kubernetes Secrets.
func (p *ImagePuller) buildKeychain(ctx context.Context, secretNames []string, namespace string) (authn.Keychain, error) {
	if len(secretNames) == 0 {
		return authn.DefaultKeychain, nil
	}

	var keychains []authn.Keychain
	for _, secretName := range secretNames {
		secret := &corev1.Secret{}
		if err := p.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, secret); err != nil {
			return nil, fmt.Errorf("getting pull secret %q in namespace %q: %w", secretName, namespace, err)
		}

		kc, err := keychainFromSecret(secret)
		if err != nil {
			return nil, fmt.Errorf("building keychain from secret %q: %w", secretName, err)
		}
		if kc != nil {
			keychains = append(keychains, kc)
		}
	}

	if len(keychains) == 0 {
		return authn.DefaultKeychain, nil
	}
	return authn.NewMultiKeychain(append(keychains, authn.DefaultKeychain)...), nil
}

// keychainFromSecret builds an authn.Keychain from a single Kubernetes docker registry secret.
func keychainFromSecret(secret *corev1.Secret) (authn.Keychain, error) {
	var configData []byte

	switch secret.Type {
	case corev1.SecretTypeDockerConfigJson:
		configData = secret.Data[corev1.DockerConfigJsonKey]
	case corev1.SecretTypeDockercfg:
		// Wrap legacy .dockercfg format into .docker/config.json format
		raw := secret.Data[corev1.DockerConfigKey]
		if len(raw) == 0 {
			return nil, nil
		}
		wrapped := map[string]json.RawMessage{"auths": raw}
		var err error
		configData, err = json.Marshal(wrapped)
		if err != nil {
			return nil, fmt.Errorf("wrapping dockercfg: %w", err)
		}
	default:
		return nil, nil
	}

	if len(configData) == 0 {
		return nil, nil
	}

	var cfg dockerConfigJSON
	if err := json.Unmarshal(configData, &cfg); err != nil {
		return nil, fmt.Errorf("parsing docker config: %w", err)
	}
	return &secretKeychain{auths: cfg.Auths}, nil
}

// dockerConfigJSON represents the structure of a Docker config.json file.
type dockerConfigJSON struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

type dockerAuthEntry struct {
	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// secretKeychain implements authn.Keychain by resolving credentials from parsed docker config.
type secretKeychain struct {
	auths map[string]dockerAuthEntry
}

func (sk *secretKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	registry := resource.RegistryStr()

	// Try exact match first, then with common variations
	for _, candidate := range []string{registry, "https://" + registry, "http://" + registry} {
		if entry, ok := sk.auths[candidate]; ok {
			return authn.FromConfig(authn.AuthConfig{
				Auth:     entry.Auth,
				Username: entry.Username,
				Password: entry.Password,
			}), nil
		}
	}
	return authn.Anonymous, nil
}
