#!/bin/bash

set -e

# Get the latest tag, default to v0.0.0 if none exists
LATEST_TAG=$(git tag --sort=-v:refname | head -1)
if [ -z "$LATEST_TAG" ]; then
    LATEST_TAG="v0.0.0"
fi

# Strip 'v' prefix and parse version
VERSION="${LATEST_TAG#v}"
IFS='.' read -r MAJOR MINOR PATCH <<< "$VERSION"

# Increment patch by 1
PATCH=$((PATCH + 1))
NEW_TAG="v${MAJOR}.${MINOR}.${PATCH}"

# Stage all changes
git add -A

# Check if there are changes to commit
if git diff --cached --quiet; then
    echo "No changes to commit."
else
    git commit -m "release: ${NEW_TAG}"
    echo "Committed changes."
fi

# Create and push tag
git tag -a "$NEW_TAG" -m "$NEW_TAG"
git push origin main
git push origin "$NEW_TAG"

echo "Released ${NEW_TAG}"
