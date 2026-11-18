# terraform-provider-maple

## 0.2.0

### Minor Changes

- 1c142ef: A `maple_status_page` resource: a status page's settings, theme, components and visibility, applied
  through the page's config document, plus its password and custom domain, set through their own endpoints. Components keep their IDs across edits by
  name, and an unset `manual_status` leaves a responder's override alone. `custom_domain_dns_records`
  lists the records to create at your DNS provider.

### Patch Changes

- f1e1c8a: A `WithUserAgent` option for hosts that embed the provider, so the Pulumi provider's API calls
  identify as `pulumi-maple/<version>` instead of Terraform. Terraform's own User-Agent is unchanged.

## 0.1.0

### Minor Changes

- dc46d50: First release: dashboards, alert rules, alert destinations, uptime monitors, scrape targets and API keys as resources, and the ingest keys data source.
