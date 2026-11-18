resource "maple_status_page" "acme" {
  name        = "Acme Status"
  slug        = "acme-status"
  description = "Live status of Acme's API and dashboard."
  website_url = "https://acme.example"
  support_url = "https://acme.example/support"
  logo_url    = "data:image/png;base64,${filebase64("${path.module}/logo.png")}"

  auto_incidents = "draft"
  theme = {
    preset     = "midnight"
    appearance = "dark"
    accent     = "#a5b4fc"
  }

  components = [
    { name = "API", monitor_id = maple_uptime_monitor.api.id },
    { name = "Dashboard", group = "Websites" },
    { name = "Docs", group = "Websites", show_uptime = false },
  ]

  custom_domain = "status.acme.example"
}

# The DNS records the custom domain needs, here at Cloudflare. Their values are
# known once the page exists: on the first run, apply the page with -target first.
resource "cloudflare_dns_record" "status" {
  for_each = { for r in maple_status_page.acme.custom_domain_dns_records : "${r.type}:${r.name}" => r }

  zone_id = var.cloudflare_zone_id
  type    = each.value.type
  name    = each.value.name
  content = each.value.value
  ttl     = 300
}
