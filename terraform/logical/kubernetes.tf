resource "kubernetes_namespace" "kots_app" {
  metadata {
    name = local.k8s_namespace_name
  }
}

resource "kubernetes_role" "dozuki_subsite_role" {
  metadata {
    name      = "dozuki_subsite_role"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }

  rule {
    api_groups = ["infra.dozuki.com"]
    resources  = ["subsites"]
    verbs      = ["get", "list", "watch", "create", "delete"]
  }
}


resource "kubernetes_role_binding" "dozuki_subsite_role_binding" {

  metadata {
    name      = "dozuki_subsite_role_binding"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.dozuki_subsite_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
}

resource "kubernetes_cluster_role" "dozuki_list_role" {

  metadata {
    name = "dozuki_list_role"
  }

  rule {
    api_groups = ["apps"]
    resources  = ["deployments", "daemonsets"]
    verbs      = ["get", "list", "watch"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["list"]
  }
}

resource "kubernetes_cluster_role_binding" "dozuki_list_role_binding" {

  metadata {
    name = "dozuki_list_role_binding"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.dozuki_list_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
}

resource "kubernetes_secret" "dozuki_infra_credentials" {

  metadata {
    name      = "dozuki-infra-credentials"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
  type = "Opaque"

  data = {
    master_host     = local.db_master_host
    master_user     = local.db_master_username
    master_password = local.db_master_password
    bi_host         = local.db_bi_host
    bi_user         = local.db_master_username
    bi_password     = local.db_bi_password
    memcached_host  = var.memcached_cluster_address
  }
}

resource "helm_release" "metrics_server" {
  name  = "metrics-server"
  chart = "charts/metrics-server"
}

resource "helm_release" "adot_exporter" {
  depends_on = [helm_release.metrics_server]

  name  = "adot-exporter-for-eks-on-ec2"
  chart = "${path.module}/charts/adot-exporter-for-eks-on-ec2"

  set {
    name  = "clusterName"
    value = var.eks_cluster_id
  }

  set {
    name  = "awsRegion"
    value = data.aws_region.current.name
  }

  set {
    name  = "adotCollector.daemonSet.service.metrics.receivers"
    value = "{awscontainerinsightreceiver}"
  }
  set {
    name  = "adotCollector.daemonSet.service.metrics.exporters"
    value = "{awsemf}"
  }
}

resource "helm_release" "fluent_bit_log_exporter" {
  depends_on = [helm_release.adot_exporter]

  chart = "${path.module}/charts/aws-for-fluent-bit"
  name  = "aws-for-fluent-bit"

  namespace = "amazon-metrics"

  set {
    name  = "cloudWatchLogs.region"
    value = data.aws_region.current.name
  }

  set {
    name  = "global.namespaceOverride"
    value = "amazon-metrics"
  }
}

resource "kubernetes_namespace" "cert_manager" {
  metadata {
    name = "cert-manager"
  }
}

resource "helm_release" "cert_manager" {
  name  = "cert-manager"
  chart = "${path.module}/charts/cert-manager"

  namespace = kubernetes_namespace.cert_manager.metadata[0].name

  set {
    name  = "installCRDs"
    value = "true"
  }
}

resource "helm_release" "ebs_csi_driver" {
  name  = "ebs-csi-driver"
  chart = "${path.module}/charts/aws-ebs-csi-driver"

  values = [
    file("static/ebs-csi-driver-values.yaml")
  ]

  namespace = "kube-system"

  wait = true
}