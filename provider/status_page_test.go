package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/MapleTechLabs/terraform-provider-maple/fakeapi"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestStatusPageRejectsWhatTheAPIWouldRefuseMidApply(t *testing.T) {
	api := fakeapi.New(t)
	for name, tc := range map[string]struct{ body, err string }{
		"internal with a domain": {`visibility = "internal"
  custom_domain = "status.acme.test"`, `cannot have a custom domain`},
		"duplicate names": {`components = [{ name = "API" }, { name = "API" }]`, `must be unique`},
		// The API normalizes these, so state could never match the config.
		"uppercase domain":       {`custom_domain = "Status.Acme.test"`, `must be lowercase`},
		"domain trailing dot":    {`custom_domain = "status.acme.test."`, `must be lowercase`},
		"description whitespace": {"description = \"Our status\\n\"", `must not start or end with whitespace`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config: providerBlock(api) + `
resource "maple_status_page" "acme" {
  name = "Acme Status"
  slug = "acme-status"
  ` + tc.body + `
}
`,
					ExpectError: regexp.MustCompile(tc.err),
				}},
			})
		})
	}
	if api.Count("POST", "/v2/status_pages") != 0 {
		t.Fatal("a refused config must not reach the API")
	}
}

func TestStatusPageRejectsUntrimmedNames(t *testing.T) {
	api := fakeapi.New(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(api) + `
resource "maple_status_page" "acme" {
  name       = "Acme Status"
  slug       = "acme-status"
  components = [{ name = " API " }]
}
`,
			ExpectError: regexp.MustCompile(`must not start or end with whitespace`),
		}},
	})
}

func TestStatusPageLifecycle(t *testing.T) {
	api := fakeapi.New(t)
	const page = "maple_status_page.acme"
	monitor := `
resource "maple_uptime_monitor" "api" {
  name   = "API"
  config = jsonencode({ type = "http", url = "https://api.acme.test/health" })
}
`
	initial := providerBlock(api) + monitor + `
resource "maple_status_page" "acme" {
  name          = "Acme Status"
  slug          = "acme-status"
  support_url   = "https://acme.test/support"
  visibility    = "password"
  password      = "correct horse battery"
  custom_domain = "status.acme.test"
  theme         = { preset = "midnight", accent = "#a5b4fc" }
  components = [
    { name = "API", monitor_id = maple_uptime_monitor.api.id },
    { name = "Docs", group = "Websites", show_uptime = false },
  ]
}
`
	internal := providerBlock(api) + monitor + `
resource "maple_status_page" "acme" {
  name        = "Acme Status"
  slug        = "acme-status"
  support_url = "https://acme.test/support"
  visibility  = "internal"
  components = [
    { name = "Status API" },
    { name = "API", monitor_id = maple_uptime_monitor.api.id },
  ]
}
`
	var pageID, apiComponentID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: initial,
				Check: resource.ComposeTestCheckFunc(
					resource.TestMatchResourceAttr(page, "id", regexp.MustCompile(`^stp_`)),
					captureID(page, &pageID),
					captureAttr(page, "components.0.id", &apiComponentID),
					resource.TestCheckResourceAttr(page, "visibility", "password"),
					resource.TestCheckResourceAttr(page, "theme.font", "sans"),
					resource.TestCheckResourceAttr(page, "show_powered_by", "true"),
					resource.TestCheckResourceAttr(page, "components.0.show_uptime", "true"),
					resource.TestCheckResourceAttrPair(page, "components.0.monitor_id", "maple_uptime_monitor.api", "id"),
					resource.TestCheckResourceAttr(page, "custom_domain_dns_records.#", "2"),
					resource.TestCheckResourceAttr(page, "custom_domain_dns_records.0.type", "CNAME"),
					func(*terraform.State) error {
						view, _, hostname := api.StatusPage(pageID)
						if view["draft"] != false || view["has_password"] != true || hostname != "status.acme.test" {
							return fmt.Errorf("page not published with its password and domain: %v %v", view, hostname)
						}
						return nil
					},
				),
			},
			{
				// A responder flips a component during an incident: not drift.
				PreConfig: func() {
					api.MutateStatusPage(pageID, func(config map[string]any) {
						config["components"].([]any)[0].(map[string]any)["manual_status"] = "degraded"
					})
				},
				Config:           initial,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// A theme edited in the UI is drift and is put back.
				PreConfig: func() {
					api.MutateStatusPage(pageID, func(config map[string]any) {
						config["theme"].(map[string]any)["font"] = "serif"
					})
				},
				Config: initial,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(page, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(page, "theme.font", "sans"),
			},
			{
				// A responder flips a status while the apply is in flight: the conditional
				// write fails, and the retry reads and keeps their status.
				PreConfig: func() {
					api.BeforeNextConfigWrite(pageID, func(config map[string]any) {
						config["components"].([]any)[0].(map[string]any)["manual_status"] = "major_outage"
					})
				},
				// The domain is unchanged, so its records stay known for a for_each over them.
				// Component IDs stay known too.
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue(page, tfjsonpath.New("custom_domain_dns_records"), knownvalue.ListSizeExact(2)),
					plancheck.ExpectKnownValue(page, tfjsonpath.New("components").AtSliceIndex(1).AtMapKey("id"), knownvalue.StringRegexp(regexp.MustCompile(`^stc_`))),
				}},
				Config: strings.Replace(initial, `support_url   = "https://acme.test/support"`, `support_url   = "https://acme.test/help"`, 1),
				Check: func(*terraform.State) error {
					_, config, _ := api.StatusPage(pageID)
					if status := config["components"].([]any)[0].(map[string]any)["manual_status"]; status != "major_outage" {
						return fmt.Errorf("the status set mid-apply was overwritten: %v", status)
					}
					return nil
				},
			},
			{
				// Internal needs the domain gone first; the password goes after the config.
				Config: internal,
				Check: resource.ComposeTestCheckFunc(
					sameID(page, &pageID),
					resource.TestCheckResourceAttr(page, "components.#", "2"),
					resource.TestCheckResourceAttr(page, "components.0.name", "Status API"),
					resource.TestCheckResourceAttrPtr(page, "components.1.id", &apiComponentID),
					resource.TestCheckNoResourceAttr(page, "custom_domain"),
					resource.TestCheckResourceAttr(page, "custom_domain_dns_records.#", "0"),
					resource.TestCheckResourceAttr(page, "theme.preset", "default"),
					func(*terraform.State) error {
						view, config, hostname := api.StatusPage(pageID)
						if view["visibility"] != "internal" || view["has_password"] != false || hostname != nil {
							return fmt.Errorf("page not internal without password and domain: %v %v", view, hostname)
						}
						api := config["components"].([]any)[1].(map[string]any)
						if api["manual_status"] != "major_outage" {
							return fmt.Errorf("the responder's manual status was overwritten: %v", api)
						}
						return nil
					},
				),
			},
			{
				// The override set outside Terraform imports as unset, as the config leaves it.
				ResourceName:      page,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if view, _, _ := api.StatusPage(pageID); view != nil {
				return fmt.Errorf("status page %s still exists", pageID)
			}
			return nil
		},
	})
}

func TestStatusPageRefusesRemovingThePasswordOfAPasswordPage(t *testing.T) {
	api := fakeapi.New(t)
	page := func(extra string) string {
		return providerBlock(api) + `
resource "maple_status_page" "acme" {
  name = "Acme Status"
  slug = "acme-status"
  ` + extra + `
}
`
	}
	var pageID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: page(`visibility = "password"
  password = "correct horse battery"`),
				Check: captureID("maple_status_page.acme", &pageID),
			},
			{
				// Unset visibility keeps "password", which needs the password.
				Config:      page(``),
				ExpectError: regexp.MustCompile(`Password removed from a password-protected page`),
			},
			{
				Config: page(`visibility = "public"`),
				Check: func(*terraform.State) error {
					if view, _, _ := api.StatusPage(pageID); view["visibility"] != "public" || view["has_password"] != false {
						return fmt.Errorf("page not public without a password: %v", view)
					}
					return nil
				},
			},
		},
	})
}
