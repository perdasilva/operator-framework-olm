#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REGISTRY="${REGISTRY:-quay.io/olmtest}"
CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
TAG="${TAG:-v1}"
PUSH=false

if [[ "${1:-}" == "--push" ]]; then
    PUSH=true
fi

images=(
    "lifecycle-catalog:${TAG}"
    "lifecycle-catalog-no-lifecycle:${TAG}"
)

for image_tag in "${images[@]}"; do
    image_name="${image_tag%%:*}"
    full_ref="${REGISTRY}/${image_tag}"
    echo "Building ${full_ref}..."
    ${CONTAINER_TOOL} build --platform=linux/amd64 -f "${SCRIPT_DIR}/Dockerfile" -t "${full_ref}" "${SCRIPT_DIR}/${image_name}"

    if ${PUSH}; then
        echo "Pushing ${full_ref}..."
        ${CONTAINER_TOOL} push "${full_ref}"
    fi
done

echo "Done."
