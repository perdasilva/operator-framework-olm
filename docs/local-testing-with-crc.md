# Local testing with CRC

We can use CRC as an Openshift-like environment within which to test downstream OLM. [CRC](https://developers.redhat.com/products/codeready-containers/overview) 
is a tool for deploying a local Openshift cluster on your laptop.

## Prerequisites

1. [CRC](https://developers.redhat.com/products/codeready-containers/overview) binary is visible
2. [OC](https://docs.openshift.com/container-platform/4.8/cli_reference/openshift_cli/getting-started-cli.html) binary is visible
3. Login to the registry.ci.openshift.org registry; see [this](https://docs.ci.openshift.org/docs/how-tos/use-registries-in-build-farm/#how-do-i-log-in-to-pull-images-that-require-authentication).

TL;DR

1. Install [CRC](https://developers.redhat.com/products/codeready-containers/overview)
2. Login to the Openshift CI repository: Navigate to the [console URL](https://console-openshift-console.apps.ci.l2s4.p1.openshiftapps.com/). After logging in to this cluster using the console, use the link in the top right “Copy login command” to authenticate your local oc CLI. Then you can run oc registry login to authenticate to the registry. 
3. `make crc` to provision a CRC cluster, build OLM, and deploy it on the cluster
4. `export KUBECONFIG=~/.crc/machines/crc/kubeconfig`
5. Execute e2e tests as you normally would, e.g., `make e2e/olm`

#### Gosh darn it, how does it work?

`./scripts/crc-start.sh` is used to provision a crc cluster. `./scripts/crc-deploy.sh` pushes the `olm:test` and `opm:test` to
`image-registry.openshift-image-registry.svc:5000/openshift/olm:test` and `image-registry.openshift-image-registry.svc:5000/openshift/opm:test`
images to the crc image registry under the global project `openshift` (to be independent from the olm namespace). It also generates an image stream
from these images. Finally, using the istag for the image. It also generates the olm manifests by applying generated helm values file (`values-crc-e2e.yaml`) 
and other generated yaml patches (`scripts/*.crc.e2e.patch.yaml`) to make sure the manifests point to the newly pushed images. The generated manifests are
then applied to the cluster using `kubectl replace` in priority order (lexical sort).

#### Make targets

1. `make crc-start`: provision a crc cluster, if necessary
   1. `FORCE_CLEAN=1 make crc-start`: nuke any current installation including cache and current cluster instance
2. `make crc-build`: build olm images with the right tags
3. `make crc-deploy`: generate manifests, upload olm images, deploy olm
   1. `SKIP_MANIFESTS=1 make crc-deploy`: skip manifest generation and deployment (only update images)
   2. `SKIP_WAIT_READY=1 make crc-deploy`: skip waiting for olm deployments to be available at the end
4. `make crc`: the same as `make crc-start crc-build crc-deploy`

#### Manipulating Resources

If new resources are introduced that require being updated for local deployment (e.g. updating the pod spec image) follow
the pattern used in `scripts/crc-deploy.sh:make_manifest_patches`