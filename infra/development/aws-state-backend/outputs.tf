output "bucket_name" {
  description = "S3 bucket to place in cap-002-tenant-space-config."
  value       = aws_s3_bucket.terraform_state.id
}

output "aws_region" {
  description = "AWS region to place in cap-002-tenant-space-config."
  value       = var.aws_region
}

output "state_prefix" {
  description = "State prefix to place in cap-002-tenant-space-config."
  value       = var.state_prefix
}

output "iam_user_name" {
  description = "IAM user for which an operator must create an access key."
  value       = aws_iam_user.workflow.name
}

output "iam_policy_arn" {
  description = "Least-privilege state policy attached to the workflow IAM user."
  value       = aws_iam_policy.workflow_state.arn
}
