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
