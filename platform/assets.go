// Package assets embeds the reference Terraform modules and Ansible
// playbooks for Urbex's base platform services (Gitea, Komodo,
// Technitium, Keycloak, observability). urbex bootstrap materializes
// these into the target GitOps repo before running terraform/ansible
// against them. See docs/decisions/0017-embedded-base-infra-assets.md in
// urbforge/urbex.
package assets

import "embed"

//go:embed all:terraform
var Terraform embed.FS

//go:embed all:ansible
var Ansible embed.FS

// Dockerfiles holds the per-runtime Dockerfiles (go, java, python) Komodo
// builds project services with when they don't bring their own - see
// docs/decisions/0018-image-build-komodo-gitea-registry.md in urbforge/urbex.
//
//go:embed dockerfiles
var Dockerfiles embed.FS

// Komodo holds the scripts urbex installs into Komodo itself: the Action
// that builds release tags - see
// docs/decisions/0020-trunk-releases-gitops-environments.md in
// urbforge/urbex.
//
//go:embed komodo
var Komodo embed.FS
