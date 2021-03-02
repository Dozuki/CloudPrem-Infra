#!/bin/bash
set -eoxu pipefail

GENERATE_SCRIPT='./kubernetes-yml-generate'
REPLICATED_VERSION='2.40.4'

curl -s "https://get.replicated.com/kubernetes-yml-generate?replicated_tag=$REPLICATED_VERSION" -o "$GENERATE_SCRIPT"

chmod +x "$GENERATE_SCRIPT"

$GENERATE_SCRIPT \
 ui_bind_port=32001 \
 storage_class=gp2 \
 > "charts/replicated/templates/replicated.yaml"

rm "$GENERATE_SCRIPT"

# Adding the license voumes
yq e 'select(.kind == "Deployment").spec.template.spec.volumes += [{"name": "replicated-license", "secret": { "secretName": "{{ .Values.license_secret }}"}},{"name": "load-license", "configMap": { "name": "load-license", "defaultMode": 0777}}]' -i charts/replicated/templates/replicated.yaml
yq e '(select(.kind == "Deployment").spec.template.spec.containers[] | select(.name == "replicated").volumeMounts) += [{"name": "replicated-license", "mountPath": "/tmp/license.rli", "subPath": "license.rli"}, {"name": "load-license", "mountPath": "/tmp/load.sh", "subPath": "load.sh"}]' -i charts/replicated/templates/replicated.yaml
yq e '(select(.kind == "Deployment").spec.template.spec.containers[] | select(.name == "replicated").lifecycle) = {"postStart": {"exec": { "command": ["/bin/sh", "-c", "/tmp/load.sh"]}}}' -i charts/replicated/templates/replicated.yaml