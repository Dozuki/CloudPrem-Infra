output "eks_cluster_id" {
  value = module.eks_cluster.cluster_id
}
output "eks_cluster_access_role_arn" {
  value = module.cluster_access_role.iam_role_arn
}
output "nlb_dns_name" {
  value = module.nlb.lb_dns_name
}
output "cluster_primary_sg" {
  value = module.eks_cluster.cluster_primary_security_group_id
}
//output "health_check_type" {
//  value = data.aws_autoscaling_group.workers.health_check_type
//}