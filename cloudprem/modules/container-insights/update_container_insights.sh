#!/bin/bash

curl https://raw.githubusercontent.com/aws-samples/amazon-cloudwatch-container-insights/latest/k8s-deployment-manifest-templates/deployment-mode/daemonset/container-insights-monitoring/quickstart/cwagent-fluentd-quickstart.yaml | sed "s/{{cluster_name}}/{{ .Values.cluster_name }}/;s/{{region_name}}/{{ .Values.region_name }}/" > container_insights/templates/cwagent-fluentd.yaml