locals {
  tenant_state_prefix = "${var.state_prefix}/tenant-space"
  default_tags = {
    Project   = "harvester-upgrade-test-suite"
    ManagedBy = "terraform"
    Purpose   = "terraform-state"
  }
  tags = merge(local.default_tags, var.tags)
}

resource "aws_s3_bucket" "terraform_state" {
  bucket = var.bucket_name
  tags   = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_ownership_controls" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  depends_on = [aws_s3_bucket_versioning.terraform_state]

  rule {
    id     = "expire-cap002-state"
    status = "Enabled"

    filter {
      prefix = "${local.tenant_state_prefix}/"
    }

    expiration {
      days = var.retention_days
    }

    noncurrent_version_expiration {
      noncurrent_days = var.retention_days
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  rule {
    id     = "remove-expired-delete-markers"
    status = "Enabled"

    filter {
      prefix = "${local.tenant_state_prefix}/"
    }

    expiration {
      expired_object_delete_marker = true
    }
  }
}

data "aws_iam_policy_document" "require_tls" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.terraform_state.arn,
      "${aws_s3_bucket.terraform_state.arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id
  policy = data.aws_iam_policy_document.require_tls.json

  depends_on = [aws_s3_bucket_public_access_block.terraform_state]
}

resource "aws_iam_user" "workflow" {
  name = var.iam_user_name
  path = "/harvester-upgrade-test-suite/"
  tags = local.tags
}

data "aws_iam_policy_document" "workflow_state" {
  statement {
    sid       = "ListTenantSpaceState"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.terraform_state.arn]

    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["${local.tenant_state_prefix}/*"]
    }
  }

  statement {
    sid    = "ReadWriteTenantSpaceState"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
    ]
    resources = [
      "${aws_s3_bucket.terraform_state.arn}/${local.tenant_state_prefix}/*/terraform.tfstate",
    ]
  }

  statement {
    sid    = "ManageTenantSpaceStateLocks"
    effect = "Allow"
    actions = [
      "s3:DeleteObject",
      "s3:GetObject",
      "s3:PutObject",
    ]
    resources = [
      "${aws_s3_bucket.terraform_state.arn}/${local.tenant_state_prefix}/*/terraform.tfstate.tflock",
    ]
  }
}

resource "aws_iam_policy" "workflow_state" {
  name        = "${var.iam_user_name}-policy"
  description = "Prefix-scoped access to CAP-002 Terraform state and lock files."
  policy      = data.aws_iam_policy_document.workflow_state.json
  tags        = local.tags
}

resource "aws_iam_user_policy_attachment" "workflow_state" {
  user       = aws_iam_user.workflow.name
  policy_arn = aws_iam_policy.workflow_state.arn
}
