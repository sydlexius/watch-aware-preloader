#!/bin/bash
# Print the changelog text for a release tag: the tag message's subject and body
# only. Used by the release workflow to fill the .plg <CHANGES> block.
#
# Deliberately NOT %(contents): for a signed annotated tag that includes the
# SSH/PGP signature armor (#171), which would then ship to every installer.
# %(contents:subject) and %(contents:body) exclude %(contents:signature).
#
# Usage: plugin/tag-changelog.sh <tag>
# Prints nothing if the tag has no message (the caller supplies a fallback).
set -euo pipefail

tag="${1:?usage: tag-changelog.sh <tag>}"

subject="$(git tag -l --format='%(contents:subject)' "${tag}")"
body="$(git tag -l --format='%(contents:body)' "${tag}")"

out="${subject}"
if [ -n "${body}" ]; then
    out="${out}"$'\n\n'"${body}"
fi
printf '%s' "${out}"
