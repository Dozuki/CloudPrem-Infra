#!/usr/bin/env bash
# terraform-docs for terraform/logical.
#
# Why this shim exists: the logical layer uses OpenTofu `provider for_each` (indexed
# provider references like `azurerm.main[each.key]`). terraform-docs' parser follows
# Terraform grammar and aborts on that construct ("Invalid provider reference", still true
# as of v0.24.0), so the standard terraform-docs hook cannot run here. We generate the docs
# from a throwaway copy of the .tf files with only those `provider =` meta-argument lines
# removed. Inputs/Outputs/Requirements come out exact; the only imprecision is that the 3
# affected azure resources are attributed to the default azurerm provider instead of the
# aliased instance. Delete this shim and restore the standard hook once terraform-docs
# supports OpenTofu provider-for_each.
set -euo pipefail

layer="terraform/logical"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cp "$layer"/*.tf "$layer/README.md" "$tmp/"
# Drop only the indexed-provider meta-argument lines (keep the resources themselves).
sed -i.bak '/provider[[:space:]]*=[[:space:]]*azurerm\.main\[/d' "$tmp"/*.tf
rm -f "$tmp"/*.bak

# Inject into the copied README (respects its BEGIN/END markers + any hand-written prose).
terraform-docs markdown table --output-file README.md "$tmp" >/dev/null

if ! cmp -s "$tmp/README.md" "$layer/README.md"; then
  cp "$tmp/README.md" "$layer/README.md"
  echo "terraform/logical/README.md regenerated - stage it and re-commit."
  exit 1
fi
