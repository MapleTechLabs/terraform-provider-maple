package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/MapleTechLabs/terraform-provider-maple/internal/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// statusPageResource manages a status page through its config document
// (`PUT /v2/status_pages/{id}/config`), plus the password and custom domain,
// which have their own endpoints.
type statusPageResource struct {
	client *client.Client
}

var (
	_ resource.Resource                   = (*statusPageResource)(nil)
	_ resource.ResourceWithConfigure      = (*statusPageResource)(nil)
	_ resource.ResourceWithImportState    = (*statusPageResource)(nil)
	_ resource.ResourceWithValidateConfig = (*statusPageResource)(nil)
)

func newStatusPageResource() resource.Resource { return &statusPageResource{} }

type statusPageModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	Slug                    types.String `tfsdk:"slug"`
	Description             types.String `tfsdk:"description"`
	WebsiteURL              types.String `tfsdk:"website_url"`
	LogoURL                 types.String `tfsdk:"logo_url"`
	LogoDarkURL             types.String `tfsdk:"logo_dark_url"`
	FaviconURL              types.String `tfsdk:"favicon_url"`
	SupportURL              types.String `tfsdk:"support_url"`
	PrivacyURL              types.String `tfsdk:"privacy_url"`
	TermsURL                types.String `tfsdk:"terms_url"`
	ShowPoweredBy           types.Bool   `tfsdk:"show_powered_by"`
	AutoIncidents           types.String `tfsdk:"auto_incidents"`
	Visibility              types.String `tfsdk:"visibility"`
	Theme                   types.Object `tfsdk:"theme"`
	Components              types.List   `tfsdk:"components"`
	Password                types.String `tfsdk:"password"`
	CustomDomain            types.String `tfsdk:"custom_domain"`
	CustomDomainCNAMETarget types.String `tfsdk:"custom_domain_cname_target"`
	CustomDomainDNSRecords  types.List   `tfsdk:"custom_domain_dns_records"`
}

type statusPageThemeModel struct {
	Preset     types.String `tfsdk:"preset"`
	Appearance types.String `tfsdk:"appearance"`
	Accent     types.String `tfsdk:"accent"`
	Background types.String `tfsdk:"background"`
	Text       types.String `tfsdk:"text"`
	Card       types.String `tfsdk:"card"`
	Border     types.String `tfsdk:"border"`
	Radius     types.String `tfsdk:"radius"`
	Font       types.String `tfsdk:"font"`
}

type statusPageComponentModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Group        types.String `tfsdk:"group"`
	MonitorID    types.String `tfsdk:"monitor_id"`
	ManualStatus types.String `tfsdk:"manual_status"`
	ShowUptime   types.Bool   `tfsdk:"show_uptime"`
}

var (
	themeAttrTypes = map[string]attr.Type{
		"preset": types.StringType, "appearance": types.StringType, "accent": types.StringType,
		"background": types.StringType, "text": types.StringType, "card": types.StringType,
		"border": types.StringType, "radius": types.StringType, "font": types.StringType,
	}
	componentAttrTypes = map[string]attr.Type{
		"id": types.StringType, "name": types.StringType, "description": types.StringType,
		"group": types.StringType, "monitor_id": types.StringType, "manual_status": types.StringType,
		"show_uptime": types.BoolType,
	}
	dnsRecordAttrTypes = map[string]attr.Type{
		"type": types.StringType, "name": types.StringType, "value": types.StringType, "purpose": types.StringType,
	}
	// The API's default theme; an unset theme field takes its value.
	defaultTheme = map[string]string{"preset": "default", "appearance": "auto", "radius": "md", "font": "sans"}
	hexColor     = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	// The API trims names; a name it would change could never match its state.
	trimmed = []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S(.*\S)?$`), "must not start or end with whitespace")}
)

func (r *statusPageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status_page"
}

func (r *statusPageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optionalText := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Description: description, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}}
	}
	choice := func(description, def string, values ...string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Description: description, Default: stringdefault.StaticString(def), Validators: oneOf(values...)}
	}
	color := func(what string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional:    true,
			Description: what + " as lowercase `#rrggbb`. Unset uses the preset's. Applies only when `appearance` is `light` or `dark`.",
			Validators:  []validator.String{stringvalidator.RegexMatches(hexColor, "must be a lowercase #rrggbb colour")},
		}
	}
	themeDefault := map[string]attr.Value{}
	for name := range themeAttrTypes {
		themeDefault[name] = types.StringNull()
		if value, ok := defaultTheme[name]; ok {
			themeDefault[name] = types.StringValue(value)
		}
	}
	image := "An https URL, or an uploaded image as a base64 `data:image/...` URL of at most 128 KB, e.g. `\"data:image/png;base64,${filebase64(\"logo.png\")}\"`."
	resp.Schema = schema.Schema{
		Description: "A public status page: its settings, theme, components, visibility, password and custom domain. " +
			"Incidents are operational and not managed here. Needs an API key with the `status_pages:write` scope; " +
			"`visibility` and `password` also need an org admin.",
		Attributes: map[string]schema.Attribute{
			"id":            idAttribute("stp"),
			"name":          schema.StringAttribute{Required: true, Description: "Shown as the page title.", Validators: trimmed},
			"slug":          schema.StringAttribute{Required: true, Description: "The page's address, `<slug>.<status domain>`: 3 to 48 lowercase letters, digits or inner hyphens. Unique across Maple."},
			"description":   optionalText("Text under the title."),
			"website_url":   optionalText("Your site, linked from the page."),
			"logo_url":      optionalText("The logo. " + image),
			"logo_dark_url": optionalText("A logo shown in dark mode instead of `logo_url`. " + image),
			"favicon_url":   optionalText("The browser tab icon. " + image),
			"support_url":   optionalText("An https link to your support page, shown in the header."),
			"privacy_url":   optionalText("An https link to your privacy policy, shown in the footer."),
			"terms_url":     optionalText("An https link to your terms of service, shown in the footer."),
			"show_powered_by": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the page credits Maple. Defaults to `true`.",
			},
			"auto_incidents": choice("Whether a linked monitor going down opens a page incident: `off` (default), `draft` (unpublished until you publish it) or `publish`.", "off", "off", "draft", "publish"),
			"visibility": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Who can read the page: `public`, `password` (needs `password`) or `internal` (only your organization's members, in Maple; incompatible with `custom_domain`). " +
					"Unset keeps the page's visibility, `public` for a new page. Changing it needs an org admin.",
				Validators:    oneOf("public", "password", "internal"),
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"theme": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				Description: "How the page looks. Unset fields take the default theme's value. Text must reach a WCAG AA contrast of 4.5:1.",
				Default:     objectdefault.StaticValue(types.ObjectValueMust(themeAttrTypes, themeDefault)),
				Attributes: map[string]schema.Attribute{
					"preset":     choice("Base palette: `default`, `maple`, `mono`, `midnight` or `paper`.", "default", "default", "maple", "mono", "midnight", "paper"),
					"appearance": choice("`auto` (default) follows the visitor's setting; `light` or `dark` forces one and enables the colour overrides.", "auto", "auto", "light", "dark"),
					"accent": schema.StringAttribute{
						Optional:    true,
						Description: "Link and highlight colour as lowercase `#rrggbb`, in every appearance. Unset uses the preset's.",
						Validators:  []validator.String{stringvalidator.RegexMatches(hexColor, "must be a lowercase #rrggbb colour")},
					},
					"background": color("Page background"),
					"text":       color("Text colour"),
					"card":       color("Card and group header background"),
					"border":     color("Border colour"),
					"radius":     choice("Corner radius: `none`, `sm`, `md` (default) or `lg`.", "md", "none", "sm", "md", "lg"),
					"font":       choice("Font family: `sans` (default), `serif` or `mono`.", "sans", "sans", "serif", "mono"),
				},
			},
			"components": schema.ListNestedAttribute{
				Optional: true, Computed: true,
				Description: "Every component, in display order (at most 200). Components are matched to the page's existing ones by name, so their IDs, incidents and uptime history survive edits; a renamed one is recreated, and one left out is deleted.",
				Default:     listdefault.StaticValue(types.ListValueMust(types.ObjectType{AttrTypes: componentAttrTypes}, []attr.Value{})),
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          schema.StringAttribute{Computed: true, Description: "The `stc_…` component ID, for incidents posted through the API."},
					"name":        schema.StringAttribute{Required: true, Description: "Component name.", Validators: trimmed},
					"description": optionalText("Shown with the component."),
					"group":       optionalText("Heading the component is listed under."),
					"monitor_id":  schema.StringAttribute{Optional: true, Description: "The uptime monitor driving its status and uptime bars, typically `maple_uptime_monitor.<name>.id`."},
					"manual_status": schema.StringAttribute{
						Optional: true,
						Description: "A manual status override: `operational`, `maintenance`, `degraded`, `partial_outage` or `major_outage`. " +
							"Unset leaves the override as responders set it in Maple, so an apply during an incident does not undo it.",
						Validators: oneOf("operational", "maintenance", "degraded", "partial_outage", "major_outage"),
					},
					"show_uptime": schema.BoolAttribute{
						Optional: true, Computed: true, Default: booldefault.StaticBool(true),
						Description: "Whether the page shows its 90 days of uptime bars. Defaults to `true`.",
					},
				}},
			},
			"password": schema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "The password visitors of a `password` page enter, 8 to 128 characters. Stored by Maple only as a hash, so Terraform cannot see it change outside Terraform. Removing it removes the password. Needs an org admin.",
				Validators:  []validator.String{stringvalidator.LengthBetween(8, 128)},
			},
			"custom_domain": schema.StringAttribute{
				Optional:    true,
				Description: "Also serve the page on this hostname, e.g. `status.acme.com`. Add the records in `custom_domain_dns_records` at your DNS provider. Removing it removes the domain.",
			},
			"custom_domain_cname_target": schema.StringAttribute{
				Computed:      true,
				Description:   "The hostname to CNAME `custom_domain` to.",
				PlanModifiers: []planmodifier.String{sameDomainKeepsState{}},
			},
			"custom_domain_dns_records": schema.ListNestedAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.List{sameDomainKeepsState{}},
				Description:   "The DNS records `custom_domain` needs: the `routing` CNAME, and `ownership` and `certificate` TXT records while the domain is verified.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type":    schema.StringAttribute{Computed: true, Description: "`CNAME` or `TXT`."},
					"name":    schema.StringAttribute{Computed: true, Description: "The record name."},
					"value":   schema.StringAttribute{Computed: true, Description: "The record value."},
					"purpose": schema.StringAttribute{Computed: true, Description: "`routing`, `ownership` or `certificate`."},
				}},
			},
		},
	}
}

func (r *statusPageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func pagePath(id string) string { return "/v2/status_pages/" + url.PathEscape(id) }

func (r *statusPageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan statusPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("visibility"), &plan.Visibility)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A draft, so no half-configured page is ever served; published once configured.
	page, err := r.client.Post(ctx, "/v2/status_pages", map[string]any{
		"name": plan.Name.ValueString(), "slug": plan.Slug.ValueString(), "publish": false,
	})
	if err != nil {
		resp.Diagnostics.AddError("Creating status page", err.Error())
		return
	}
	id := fmt.Sprint(page["id"])
	// Saved as tainted on any later failure, so the next apply replaces the half-made
	// page instead of orphaning it on its slug.
	tainted := func(summary, detail string) {
		plan.ID = types.StringValue(id)
		resp.Diagnostics.Append(resp.State.Set(ctx, nullUnknowns(plan))...)
		resp.Diagnostics.AddError(summary, detail)
	}
	if _, err := r.apply(ctx, id, plan, nil); err != nil {
		tainted("Configuring status page "+id, err.Error())
		return
	}
	if _, err := r.client.Post(ctx, pagePath(id)+"/publish", nil); err != nil {
		tainted("Publishing status page "+id, err.Error())
		return
	}
	var diags diag.Diagnostics
	r.readInto(ctx, id, plan, false, &resp.State, &diags)
	if diags.HasError() {
		tainted("Reading status page "+id, fmt.Sprint(diags.Errors()))
		return
	}
	resp.Diagnostics.Append(diags...)
}

func (r *statusPageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior statusPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.Get(ctx, pagePath(prior.ID.ValueString())); client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	r.readInto(ctx, prior.ID.ValueString(), prior, false, &resp.State, &resp.Diagnostics)
}

func (r *statusPageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior statusPageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	// Sent only when configured: a value carried over from state could undo a change made since.
	configured := plan
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("visibility"), &configured.Visibility)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := prior.ID.ValueString()
	passwordSet, err := r.apply(ctx, id, configured, &prior)
	var diags diag.Diagnostics
	if err == nil {
		r.readInto(ctx, id, plan, true, &resp.State, &diags)
	} else {
		diags.AddError("Updating status page "+id, err.Error())
	}
	// On failure state stays the prior one; a password already changed must not read as the old one.
	if diags.HasError() && passwordSet {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("password"), plan.Password)...)
	}
	resp.Diagnostics.Append(diags...)
}

func (r *statusPageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var prior statusPageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, pagePath(prior.ID.ValueString())); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Deleting status page "+prior.ID.ValueString(), err.Error())
	}
}

func (r *statusPageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// apply writes plan over the page in the order the API's rules need: a page can
// only turn `password` once it has one, and only turn `internal` without a
// custom domain.
// It reports whether it changed the password, which state cannot read back.
func (r *statusPageResource) apply(ctx context.Context, id string, plan statusPageModel, prior *statusPageModel) (passwordSet bool, err error) {
	base := pagePath(id)
	page, err := r.client.Get(ctx, base)
	if err != nil {
		return false, err
	}
	hasPassword := page["has_password"] == true
	priorPassword, priorDomain := types.StringNull(), types.StringNull()
	if prior != nil {
		priorPassword, priorDomain = prior.Password, prior.CustomDomain
	}

	if !plan.Password.IsNull() && (!hasPassword || !plan.Password.Equal(priorPassword)) {
		if _, err := r.client.Put(ctx, base+"/password", map[string]any{"password": plan.Password.ValueString()}); err != nil {
			return false, fmt.Errorf("setting the password: %w", err)
		}
		passwordSet = true
	}
	domain, err := r.client.Get(ctx, base+"/custom_domain")
	if err != nil {
		return passwordSet, err
	}
	if plan.CustomDomain.IsNull() && !priorDomain.IsNull() && domain["hostname"] != nil {
		if err := r.client.Delete(ctx, base+"/custom_domain"); err != nil {
			return passwordSet, fmt.Errorf("removing the custom domain: %w", err)
		}
	}

	// Conditional on the version read, so a manual status a responder sets meanwhile
	// is not overwritten with the one read: a conflict reads the config again.
	for attempt := 0; ; attempt++ {
		current, err := r.client.Get(ctx, base+"/config")
		if err != nil {
			return passwordSet, err
		}
		body, err := configBody(ctx, plan, current)
		if err != nil {
			return passwordSet, err
		}
		_, err = r.client.Put(ctx, base+"/config?if_version="+url.QueryEscape(fmt.Sprint(current["version"])), body)
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict && attempt < 3 {
			continue
		}
		if err != nil {
			return passwordSet, fmt.Errorf("applying the config: %w", err)
		}
		break
	}

	if !plan.CustomDomain.IsNull() && domain["hostname"] != plan.CustomDomain.ValueString() {
		if _, err := r.client.Put(ctx, base+"/custom_domain", map[string]any{"hostname": plan.CustomDomain.ValueString()}); err != nil {
			return passwordSet, fmt.Errorf("setting the custom domain: %w", err)
		}
	}
	if plan.Password.IsNull() && !priorPassword.IsNull() && hasPassword {
		if err := r.client.Delete(ctx, base+"/password"); err != nil {
			return passwordSet, fmt.Errorf("removing the password: %w", err)
		}
	}
	return passwordSet, nil
}

func optionalString(v types.String) any {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return v.ValueString()
}

// configBody builds the config document. Components keep the IDs of the page's
// components of the same name, and an unset manual status keeps the page's.
func configBody(ctx context.Context, plan statusPageModel, current client.Object) (map[string]any, error) {
	var theme statusPageThemeModel
	if diags := plan.Theme.As(ctx, &theme, basetypes.ObjectAsOptions{}); diags.HasError() {
		return nil, fmt.Errorf("reading theme: %v", diags)
	}
	var components []statusPageComponentModel
	if diags := plan.Components.ElementsAs(ctx, &components, false); diags.HasError() {
		return nil, fmt.Errorf("reading components: %v", diags)
	}
	existing, _ := current["components"].([]any)
	claimed := make([]bool, len(existing))
	wire := make([]any, 0, len(components))
	for _, component := range components {
		item := map[string]any{
			"name":          component.Name.ValueString(),
			"description":   optionalString(component.Description),
			"group":         optionalString(component.Group),
			"monitor_id":    optionalString(component.MonitorID),
			"manual_status": optionalString(component.ManualStatus),
			"show_uptime":   component.ShowUptime.ValueBool(),
		}
		for i, raw := range existing {
			match, _ := raw.(map[string]any)
			if claimed[i] || match["name"] != component.Name.ValueString() {
				continue
			}
			claimed[i] = true
			item["id"] = match["id"]
			if component.ManualStatus.IsNull() {
				item["manual_status"] = match["manual_status"]
			}
			break
		}
		wire = append(wire, item)
	}
	body := map[string]any{
		"name":            plan.Name.ValueString(),
		"slug":            plan.Slug.ValueString(),
		"description":     optionalString(plan.Description),
		"website_url":     optionalString(plan.WebsiteURL),
		"logo_url":        optionalString(plan.LogoURL),
		"logo_dark_url":   optionalString(plan.LogoDarkURL),
		"favicon_url":     optionalString(plan.FaviconURL),
		"support_url":     optionalString(plan.SupportURL),
		"privacy_url":     optionalString(plan.PrivacyURL),
		"terms_url":       optionalString(plan.TermsURL),
		"show_powered_by": plan.ShowPoweredBy.ValueBool(),
		"auto_incidents":  plan.AutoIncidents.ValueString(),
		"theme": map[string]any{
			"preset": theme.Preset.ValueString(), "appearance": theme.Appearance.ValueString(),
			"accent": optionalString(theme.Accent), "background": optionalString(theme.Background),
			"text": optionalString(theme.Text), "card": optionalString(theme.Card), "border": optionalString(theme.Border),
			"radius": theme.Radius.ValueString(), "font": theme.Font.ValueString(),
		},
		"components": wire,
	}
	// Unset keeps the page's visibility.
	if v := optionalString(plan.Visibility); v != nil {
		body["visibility"] = v
	}
	return body, nil
}

// readInto writes the page as the API holds it. base is what the user asked for
// (the plan, or prior state): the password is never returned, and a manual status
// the user left unset stays unset.
func (r *statusPageResource) readInto(ctx context.Context, id string, base statusPageModel, keepPlannedDomain bool, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	pagePrefix := pagePath(id)
	page, err := r.client.Get(ctx, pagePrefix)
	if err != nil {
		diags.AddError("Reading status page "+id, err.Error())
		return
	}
	config, err := r.client.Get(ctx, pagePrefix+"/config")
	if err != nil {
		diags.AddError("Reading status page "+id, err.Error())
		return
	}
	domain, err := r.client.Get(ctx, pagePrefix+"/custom_domain")
	if err != nil {
		diags.AddError("Reading status page "+id, err.Error())
		return
	}
	str := func(raw any) types.String {
		if s, ok := raw.(string); ok {
			return types.StringValue(s)
		}
		return types.StringNull()
	}

	var userComponents []statusPageComponentModel
	if !base.Components.IsNull() && !base.Components.IsUnknown() {
		diags.Append(base.Components.ElementsAs(ctx, &userComponents, false)...)
	}
	unsetStatus := map[string]int{}
	for _, c := range userComponents {
		if c.ManualStatus.IsNull() {
			unsetStatus[c.Name.ValueString()]++
		}
	}
	rawComponents, _ := config["components"].([]any)
	components := make([]attr.Value, 0, len(rawComponents))
	for _, raw := range rawComponents {
		c, _ := raw.(map[string]any)
		name := fmt.Sprint(c["name"])
		status := str(c["manual_status"])
		if unsetStatus[name] > 0 {
			unsetStatus[name]--
			status = types.StringNull()
		}
		components = append(components, types.ObjectValueMust(componentAttrTypes, map[string]attr.Value{
			"id": str(c["id"]), "name": types.StringValue(name), "description": str(c["description"]),
			"group": str(c["group"]), "monitor_id": str(c["monitor_id"]), "manual_status": status,
			"show_uptime": types.BoolValue(c["show_uptime"] != false),
		}))
	}

	themeRaw, _ := config["theme"].(map[string]any)
	themeValues := map[string]attr.Value{}
	for name := range themeAttrTypes {
		themeValues[name] = str(themeRaw[name])
	}

	rawRecords, _ := domain["dns_records"].([]any)
	records := make([]attr.Value, 0, len(rawRecords))
	for _, raw := range rawRecords {
		record, _ := raw.(map[string]any)
		records = append(records, types.ObjectValueMust(dnsRecordAttrTypes, map[string]attr.Value{
			"type": str(record["type"]), "name": str(record["name"]), "value": str(record["value"]), "purpose": str(record["purpose"]),
		}))
	}

	cnameTarget := str(domain["cname_target"])
	dnsRecords := types.ListValueMust(types.ObjectType{AttrTypes: dnsRecordAttrTypes}, records)
	// An update that kept the domain planned its records from state; changes reach state on refresh.
	if keepPlannedDomain && base.CustomDomain.Equal(str(domain["hostname"])) &&
		!base.CustomDomainDNSRecords.IsUnknown() && !base.CustomDomainCNAMETarget.IsUnknown() {
		cnameTarget, dnsRecords = base.CustomDomainCNAMETarget, base.CustomDomainDNSRecords
	}
	password := base.Password
	if page["has_password"] != true || password.IsUnknown() {
		password = types.StringNull()
	}
	diags.Append(state.Set(ctx, statusPageModel{
		ID:                      types.StringValue(id),
		Name:                    str(config["name"]),
		Slug:                    str(config["slug"]),
		Description:             str(config["description"]),
		WebsiteURL:              str(config["website_url"]),
		LogoURL:                 str(config["logo_url"]),
		LogoDarkURL:             str(config["logo_dark_url"]),
		FaviconURL:              str(config["favicon_url"]),
		SupportURL:              str(config["support_url"]),
		PrivacyURL:              str(config["privacy_url"]),
		TermsURL:                str(config["terms_url"]),
		ShowPoweredBy:           types.BoolValue(config["show_powered_by"] != false),
		AutoIncidents:           str(config["auto_incidents"]),
		Visibility:              str(config["visibility"]),
		Theme:                   types.ObjectValueMust(themeAttrTypes, themeValues),
		Components:              types.ListValueMust(types.ObjectType{AttrTypes: componentAttrTypes}, components),
		Password:                password,
		CustomDomain:            str(domain["hostname"]),
		CustomDomainCNAMETarget: cnameTarget,
		CustomDomainDNSRecords:  dnsRecords,
	})...)
}

// nullUnknowns replaces values only the API knows, for state saved after a failed create.
func nullUnknowns(m statusPageModel) statusPageModel {
	if m.Visibility.IsUnknown() {
		m.Visibility = types.StringNull()
	}
	if m.CustomDomainCNAMETarget.IsUnknown() {
		m.CustomDomainCNAMETarget = types.StringNull()
	}
	if m.CustomDomainDNSRecords.IsUnknown() {
		m.CustomDomainDNSRecords = types.ListNull(types.ObjectType{AttrTypes: dnsRecordAttrTypes})
	}
	m.Components = types.ListNull(types.ObjectType{AttrTypes: componentAttrTypes})
	return m
}

// ValidateConfig refuses what the API would refuse half way through an apply.
func (r *statusPageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config statusPageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Visibility.ValueString() == "internal" && !config.CustomDomain.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("custom_domain"), "Custom domain on an internal page",
			"An internal page is served on no address, so it cannot have a custom domain. Remove custom_domain or change visibility.")
	}
	if config.Components.IsNull() || config.Components.IsUnknown() {
		return
	}
	var components []statusPageComponentModel
	resp.Diagnostics.Append(config.Components.ElementsAs(ctx, &components, false)...)
	seen := map[string]bool{}
	for i, c := range components {
		if c.Name.IsUnknown() {
			continue
		}
		// Components are matched to the page's by name, so a name must be unique.
		if seen[c.Name.ValueString()] {
			resp.Diagnostics.AddAttributeError(path.Root("components").AtListIndex(i).AtName("name"), "Duplicate component name",
				"Component names must be unique on a page: "+c.Name.ValueString())
		}
		seen[c.Name.ValueString()] = true
	}
}

// sameDomainKeepsState keeps the custom domain's DNS records known in a plan that
// does not change the domain, so resources that iterate over them can still plan.
type sameDomainKeepsState struct{}

func (sameDomainKeepsState) Description(context.Context) string {
	return "Keeps the prior value while custom_domain is unchanged."
}

func (m sameDomainKeepsState) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (sameDomainKeepsState) unchanged(ctx context.Context, plan tfsdk.Plan, state tfsdk.State) bool {
	if state.Raw.IsNull() || plan.Raw.IsNull() {
		return false
	}
	var planned, prior types.String
	if plan.GetAttribute(ctx, path.Root("custom_domain"), &planned).HasError() ||
		state.GetAttribute(ctx, path.Root("custom_domain"), &prior).HasError() {
		return false
	}
	return !planned.IsUnknown() && planned.Equal(prior)
}

func (m sameDomainKeepsState) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.PlanValue.IsUnknown() && m.unchanged(ctx, req.Plan, req.State) {
		resp.PlanValue = req.StateValue
	}
}

func (m sameDomainKeepsState) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.PlanValue.IsUnknown() && m.unchanged(ctx, req.Plan, req.State) {
		resp.PlanValue = req.StateValue
	}
}
