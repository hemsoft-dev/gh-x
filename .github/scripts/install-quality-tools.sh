#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$script_dir/../quality-tools.env"

install_lint_tools() {
  go install "honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}"
  go install "golang.org/x/tools/cmd/deadcode@${DEADCODE_VERSION}"
  go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
  go install "github.com/go-critic/go-critic/cmd/gocritic@${GOCRITIC_VERSION}"
  go install "github.com/kisielk/errcheck@${ERRCHECK_VERSION}"
}

install_quality_tools() {
  go install "github.com/fzipp/gocyclo/cmd/gocyclo@${GOCYCLO_VERSION}"
  go install "github.com/uudashr/gocognit/cmd/gocognit@${GOCOGNIT_VERSION}"
}

install_mutation_tools() {
  go install "github.com/go-gremlins/gremlins/cmd/gremlins@${GREMLINS_VERSION}"
}

install_credential_scanner() {
  local destination
  destination="$(go env GOBIN)"
  if [[ -z "$destination" ]]; then
    destination="$(go env GOPATH)/bin"
  fi
  go run "$script_dir/credential-scan" -repo "$script_dir/../.." -install "$destination"
}

case "${1:-all}" in
  lint)
    install_lint_tools
    ;;
  quality)
    install_quality_tools
    ;;
  mutation)
    install_mutation_tools
    ;;
  credentials)
    install_credential_scanner
    ;;
  all)
    install_lint_tools
    install_quality_tools
    install_mutation_tools
    install_credential_scanner
    ;;
  *)
    echo "usage: $0 [lint|quality|mutation|credentials|all]" >&2
    exit 2
    ;;
esac
