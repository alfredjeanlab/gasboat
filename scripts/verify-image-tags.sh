#!/usr/bin/env bash
set -euo pipefail

# verify-image-tags.sh — Verify all gasboat pod images match the expected calver tag.
#
# Usage:
#   scripts/verify-image-tags.sh 2026.62.1
#   scripts/verify-image-tags.sh 2026.62.1 --namespace gasboat-staging

EXPECTED_VERSION="${1:-}"
NAMESPACE="${NAMESPACE:-gasboat}"

if [ -z "$EXPECTED_VERSION" ]; then
  echo "Usage: $0 <expected-calver-version> [--namespace=<ns>]"
  echo "Example: $0 2026.62.1"
  exit 1
fi

shift
for arg in "$@"; do
  case "$arg" in
    --namespace=*) NAMESPACE="${arg#*=}" ;;
    *) echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

echo "=== Image Tag Verification ==="
echo "Namespace:        ${NAMESPACE}"
echo "Expected version: ${EXPECTED_VERSION}"
echo ""

# Gather all pod names and their container images
POD_IMAGES=$(kubectl get pods -n "$NAMESPACE" \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[*].image}{"\n"}{end}')

if [ -z "$POD_IMAGES" ]; then
  echo "FAIL: No pods found in namespace ${NAMESPACE}"
  exit 1
fi

MISMATCHES=()
CHECKED=0

while IFS=$'\t' read -r pod_name images; do
  [ -z "$pod_name" ] && continue
  for img in $images; do
    # Only check gasboat-managed images (ghcr.io/groblegark)
    case "$img" in
      ghcr.io/groblegark/*)
        CHECKED=$((CHECKED + 1))
        TAG="${img##*:}"
        if [ "$TAG" = "$EXPECTED_VERSION" ]; then
          echo "  OK:   ${pod_name}  ${img}"
        else
          echo "  FAIL: ${pod_name}  ${img}  (expected tag: ${EXPECTED_VERSION})"
          MISMATCHES+=("${pod_name}: ${img} (expected :${EXPECTED_VERSION})")
        fi
        ;;
      *)
        # Skip non-gasboat images (e.g. pause, coredns)
        ;;
    esac
  done
done <<< "$POD_IMAGES"

echo ""

if [ "$CHECKED" -eq 0 ]; then
  echo "FAIL: No ghcr.io/groblegark images found in namespace ${NAMESPACE}"
  exit 1
fi

if [ ${#MISMATCHES[@]} -gt 0 ]; then
  echo "FAIL: ${#MISMATCHES[@]} image(s) do not match expected version ${EXPECTED_VERSION}:"
  for m in "${MISMATCHES[@]}"; do
    echo "  - $m"
  done
  exit 1
fi

echo "SUCCESS: All ${CHECKED} gasboat image(s) are tagged ${EXPECTED_VERSION}"
