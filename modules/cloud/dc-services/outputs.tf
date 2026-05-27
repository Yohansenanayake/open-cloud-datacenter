output "postgres_password" {
  value     = random_password.postgres.result
  sensitive = true
}

output "dcapi_url" {
  value = "http://${var.dcapi_hostname}"
}
