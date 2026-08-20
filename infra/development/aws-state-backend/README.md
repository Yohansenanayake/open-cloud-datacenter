# AWS S3 Terraform State Backend

This bootstrap stack creates the development AWS backend used by CAP-002. Its
own Terraform state remains local because it creates the remote backend.

The stack creates a private, versioned, SSE-S3-encrypted bucket, a 30-day
lifecycle for the tenant-space prefix, a TLS-only bucket policy, and a dedicated
IAM user with prefix-scoped state and lock permissions. It does not create an
access key and does not use DynamoDB.

## Bootstrap

Use temporary operator credentials that can create S3 and IAM resources:

```bash
cd infra/development/aws-state-backend
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars before continuing.
terraform init
terraform plan -out=bootstrap.tfplan
terraform apply bootstrap.tfplan
```

The committed lifecycle protection prevents an accidental Terraform destroy of
the bucket. Removing the backend requires an explicit reviewed change and
manual handling of retained state.

## Create the workflow credential

Create one access key for the IAM user returned by Terraform:

```bash
aws iam create-access-key \
  --user-name "$(terraform output -raw iam_user_name)"
```

Store the returned values in a password manager, then create the Host Secret:

```bash
kubectl -n argo create secret generic terraform-state-aws \
  --from-literal=aws-access-key-id='REPLACE_WITH_ACCESS_KEY_ID' \
  --from-literal=aws-secret-access-key='REPLACE_WITH_SECRET_ACCESS_KEY'
```

Do not place either value in Terraform variables, workflow parameters, shell
scripts, or Git. Rotate the key according to the environment's credential
policy.

Finally, copy the `bucket_name`, `aws_region`, and `state_prefix` outputs into
the CAP-002 ConfigMap.

## Retention and recovery

Both current and noncurrent objects under the tenant-space prefix are governed
by the lifecycle rules. A failed or explicitly preserved run must be recovered
before its state expires. Terraform state is sensitive and must never be copied
into the MinIO artifact bucket.
