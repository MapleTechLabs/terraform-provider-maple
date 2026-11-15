---
"terraform-provider-maple": patch
---

A `WithUserAgent` option for hosts that embed the provider, so the Pulumi provider's API calls
identify as `pulumi-maple/<version>` instead of Terraform. Terraform's own User-Agent is unchanged.
