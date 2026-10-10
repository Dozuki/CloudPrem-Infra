#!/usr/bin/env python3
"""Generate env.hcl.example from terraform/{physical,logical}/variables.tf.

Every env.hcl local is passed to both modules as an input, so the example lists every
variable either module declares, in source order, commented out with its default.

Usage: scripts/gen-env-example.py          write env.hcl.example
       scripts/gen-env-example.py --check  exit 1 if env.hcl.example is stale

Stdlib only. Parses the tofu-fmt layout (2-space attributes, 4-space validation
attributes) rather than full HCL, which is enough for variable blocks and keeps
defaults exactly as written in the source.
"""
import json
import re
import sys
import textwrap
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LAYERS = ["physical", "logical"]
OUT = ROOT / "env.hcl.example"
WIDTH = 100

# Logical inputs the deployment wrapper (infra-live logical.hcl, CPI live/) wires from
# physical outputs. They are listed by name only: a value set in env.hcl is overridden.
WIRED = {
    "azure_eso_identity_client_id", "azure_key_vault_id", "azure_key_vault_uri",
    "azure_resource_group", "azure_tenant_id", "bi_database_credential_secret",
    "db_resource_id", "dms_enabled", "dms_replication_generation", "dms_task_arn",
    "dns_domain_name", "eks_cluster_id", "nlb_http_target_group_arn",
    "nlb_https_target_group_arn", "primary_db_secret", "s3_documents_bucket",
    "s3_images_bucket", "s3_kms_key_id", "s3_objects_bucket", "s3_pdfs_bucket",
    "s3_replicate_buckets", "vault_address", "vpc_id",
}

VAR_START = re.compile(r'^variable "([A-Za-z0-9_]+)" \{\s*$')
HEREDOC = re.compile(r"^<<(-?)([A-Z]+)\s*$")

HEADER = """\
# GENERATED FILE - do not edit. Run scripts/gen-env-example.py to regenerate.
# Source: terraform/physical/variables.tf and terraform/logical/variables.tf.
# To change a description, default or order, edit the variable in those files.
#
# Every variable either module accepts, with its description and default. infra-live
# passes every env.hcl local to both the physical and logical layers, so any of these can
# be set in an env's env.hcl. Read this file at the tag your env's infra_version pins to
# see exactly what that version accepts.
#
# Everything is commented out: uncomment a line only to change it from the default.
# Some values (region, account, aws_profile, dr_region) normally come from account.hcl,
# region.hcl or root.hcl in infra-live rather than env.hcl.
locals {
"""


def attributes(lines, indent):
    """Split block body lines into (name, value_lines) at the given indent.

    A nested block (`validation {`) comes back with value_lines holding its body.
    Heredocs are consumed whole so their content is never mistaken for attributes.
    """
    pad = " " * indent
    attr = re.compile(r"^" + pad + r"([a-z_]+)\s*(=\s*(.*)|\{)\s*$")
    out, i = [], 0
    while i < len(lines):
        line = lines[i]
        m = attr.match(line)
        if not m:
            # Comments and blank lines between attributes, or a stray continuation.
            i += 1
            continue
        name, value = m.group(1), m.group(3)
        if value is None:  # nested block, ends at the closing brace at this indent
            j = i + 1
            while lines[j].rstrip() != pad + "}":
                j += 1
            out.append((name, lines[i + 1 : j]))
            i = j + 1
            continue
        body = [value]
        h = HEREDOC.match(value.strip())
        j = i + 1
        if h:
            while lines[j].strip() != h.group(2):
                body.append(lines[j])
                j += 1
            body.append(lines[j])
            j += 1
        else:
            # Multi-line values (lists, objects, parenthesised conditions) continue
            # until the next attribute at this indent.
            while j < len(lines) and not attr.match(lines[j]) and not (
                lines[j].startswith(pad + "#") and not lines[j].startswith(pad + " ")
            ):
                body.append(lines[j])
                j += 1
            while body and not body[-1].strip():
                body.pop()
        out.append((name, body))
        i = j
    return out


def text(value_lines):
    """An HCL string attribute (quoted or heredoc) as plain text."""
    first = value_lines[0].strip()
    if HEREDOC.match(first):
        return textwrap.dedent("\n".join(value_lines[1:-1])).strip()
    try:
        return json.loads(first)
    except json.JSONDecodeError:
        return first.strip('"')


def parse(layer):
    path = ROOT / "terraform" / layer / "variables.tf"
    lines = path.read_text().splitlines()
    out, i = [], 0
    while i < len(lines):
        m = VAR_START.match(lines[i])
        if not m:
            i += 1
            continue
        j = i + 1
        while lines[j] != "}":
            j += 1
        attrs = attributes(lines[i + 1 : j], 2)
        var = {"name": m.group(1), "layer": layer, "validations": []}
        for name, body in attrs:
            if name == "validation":
                var["validations"].append(dict(attributes(body, 4)))
            else:
                var[name] = body
        if "description" not in var:
            sys.exit(f"{path}: variable {var['name']} has no description")
        out.append(var)
        i = j + 1
    return out


def wrap(paragraphs):
    out = []
    for para in paragraphs.split("\n"):
        if not para.strip():
            out.append("")
        elif para.startswith((" ", "-", "*")) or len(para) <= WIDTH:
            out.append(para.rstrip())  # keep lists and preformatted lines as written
        else:
            out.extend(textwrap.wrap(para, WIDTH))
    return out


def options(var):
    """Accepted values as comment lines, from the variable's validation blocks."""
    out = []
    for v in var["validations"]:
        cond = " ".join(l.strip() for l in v.get("condition", [""]))
        m = re.fullmatch(r"contains\((\[[^\]]*\]),\s*var\." + var["name"] + r"\)", cond)
        if m:
            out.append("Possible options: " + m.group(1)[1:-1])
        elif "error_message" in v:
            out.extend(wrap("Must satisfy: " + text(v["error_message"])))
    return out


def render(var, layers):
    out = wrap(text(var["description"]))
    out.append("")
    type_lines = var.get("type", ["any"])
    out.append("Type: " + type_lines[0].strip())
    out.extend("  " + l[2:] for l in type_lines[1:])  # object types keep their layout
    out.extend(options(var))
    if var.get("sensitive", [""])[0].strip() == "true":
        out.append("Sensitive: yes, keep the real value out of git.")
    if len(layers) > 1:
        out.append("Layers: " + ", ".join(layers))
        if var["name"] in WIRED:
            out.append("The logical layer's value is wired from physical outputs; env.hcl sets physical.")
    lines = ["  #" + (" " + l if l else "") for l in out]
    if "default" in var:
        default = var["default"]
        lines.append(f"  #{var['name']} = {default[0].strip()}")
        lines.extend("  #" + l[2:] for l in default[1:])
    else:
        lines.append("  # REQUIRED: no default, set it in env.hcl or the unit terragrunt.hcl.")
        lines.append(f"  #{var['name']} =")
    return "\n".join(lines)


def generate():
    seen = {}
    for layer in LAYERS:
        for var in parse(layer):
            seen.setdefault(var["name"], (var, []))[1].append(layer)
    missing = WIRED - seen.keys()
    if missing:
        sys.exit(f"WIRED names no variable: {sorted(missing)}")
    # A wired name that physical also declares is a real env setting for physical.
    logical_only = {n for n, (_, layers) in seen.items() if layers == ["logical"]}
    blocks = [render(v, l) for v, l in seen.values() if v["name"] not in WIRED & logical_only]
    wired = [n for n in seen if n in WIRED & logical_only]
    tail = (
        "\n\n  # Set by the deployment wrapper from physical outputs, do not set in env.hcl:\n"
        + "\n".join(textwrap.wrap(", ".join(wired), WIDTH, initial_indent="  # ",
                                   subsequent_indent="  # "))
    )
    return HEADER + "\n\n".join(blocks) + tail + "\n}\n"


def main():
    content = generate()
    if "--check" in sys.argv[1:]:
        if not OUT.exists() or OUT.read_text() != content:
            print("env.hcl.example is stale: run scripts/gen-env-example.py", file=sys.stderr)
            return 1
        return 0
    OUT.write_text(content)
    return 0


if __name__ == "__main__":
    sys.exit(main())
