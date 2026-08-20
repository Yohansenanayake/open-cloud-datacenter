variable "aws_region" {
  description = "AWS region in which to create the Terraform state bucket."
  type        = string
}

variable "bucket_name" {
  description = "Globally unique S3 bucket name for test-suite Terraform state."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.bucket_name))
    error_message = "bucket_name must be a valid, lowercase S3 bucket name."
  }
}

variable "state_prefix" {
  description = "Top-level object prefix used by test-suite capabilities."
  type        = string
  default     = "capabilities"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9/-]*[a-z0-9]$", var.state_prefix))
    error_message = "state_prefix must contain lowercase letters, numbers, slashes, or hyphens without leading or trailing slashes."
  }
}

variable "retention_days" {
  description = "Days to retain current and noncurrent CAP-002 state objects."
  type        = number
  default     = 30

  validation {
    condition     = var.retention_days >= 7
    error_message = "retention_days must be at least 7."
  }
}

variable "iam_user_name" {
  description = "IAM user used by CAP-002 workflow pods."
  type        = string
  default     = "harvester-testsuite-cap002-state"
}

variable "tags" {
  description = "Additional tags to apply to AWS resources."
  type        = map(string)
  default     = {}
}
