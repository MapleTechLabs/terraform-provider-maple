---
"terraform-provider-maple": minor
---

A `maple_status_page` resource: a status page's settings, theme, components and visibility, applied
through the page's config document, plus its password and custom domain, set through their own endpoints. Components keep their IDs across edits by
name, and an unset `manual_status` leaves a responder's override alone. `custom_domain_dns_records`
lists the records to create at your DNS provider.
