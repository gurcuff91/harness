package rpc

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gurcuff91/harness/admin"
)

// admCovered maps each exported method of an admin service ("Service.Method")
// to the operation that exposes it.
var admCovered = map[string]string{
	"Sessions.List":     "admin/sessions/list",
	"Sessions.Get":      "admin/sessions/get",
	"Sessions.Messages": "admin/sessions/messages",
	"Sessions.Delete":   "admin/sessions/delete",
	"Sessions.Rename":   "admin/sessions/rename",
	"Sessions.Export":   "admin/sessions/export",
	"Sessions.ExportAs": "admin/sessions/export", // the op passes the live meta of an open session

	"Providers.List":              "admin/providers/list",
	"Providers.Models":            "admin/providers/models",
	"Providers.Model":             "admin/providers/model",
	"Providers.Connect":           "admin/providers/connect",
	"Providers.CredentialType":    "admin/providers/credential_type",
	"Providers.Disconnect":        "admin/providers/disconnect",
	"Providers.Quota":             "admin/providers/quota",
	"Providers.OAuthStart":        "admin/providers/oauth_start",
	"Providers.OAuthExchange":     "admin/providers/oauth_exchange",
	"Providers.ListCustom":        "admin/providers/list_custom",
	"Providers.PutCustom":         "admin/providers/put_custom",
	"Providers.DeleteCustom":      "admin/providers/delete_custom",
	"Providers.ReloadCustom":      "admin/providers/reload_custom",
	"Providers.RegisterOpenAI":    "admin/providers/register_openai",
	"Providers.RegisterAnthropic": "admin/providers/register_anthropic",
	"Providers.Unregister":        "admin/providers/unregister",

	"Settings.Get":              "admin/settings/get",
	"Settings.SetActiveModel":   "admin/settings/set_active_model",
	"Settings.SetThinkingLevel": "admin/settings/set_thinking_level",

	"MCP.List":       "admin/mcp/list",
	"MCP.Get":        "admin/mcp/get",
	"MCP.Put":        "admin/mcp/put",
	"MCP.Delete":     "admin/mcp/delete",
	"MCP.SetEnabled": "admin/mcp/set_enabled",

	"Memory.Configured": "admin/memory/configured",
	"Memory.Search":     "admin/memory/search",
	"Memory.List":       "admin/memory/list",
	"Memory.Get":        "admin/memory/get",
	"Memory.Put":        "admin/memory/put",
	"Memory.Delete":     "admin/memory/delete",

	"Schedules.List": "admin/schedules/list",
}

// admExcluded are the methods with no operation, each with its reason.
var admExcluded = map[string]string{
	"Sessions.ResetCount":  "internal bookkeeping: the same number is meta.reset_count in admin/sessions/get",
	"Sessions.Profiles":    "internal bookkeeping: the same list is meta.profiles in admin/sessions/get",
	"Providers.ListModels": "admin/providers/models and /model cover it (it returns Go errors per provider, not a result)",
}

func TestAdminParityEveryMethodIsDecided(t *testing.T) {
	ad := admin.New()
	t.Cleanup(func() { ad.Close() })
	ops := opIndex()
	services := map[string]any{
		"Sessions": ad.Sessions, "Providers": ad.Providers, "Settings": ad.Settings,
		"MCP": ad.MCP, "Memory": ad.Memory, "Schedules": ad.Schedules,
	}
	seen := map[string]bool{}
	for svc, v := range services {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumMethod(); i++ {
			key := svc + "." + rt.Method(i).Name
			seen[key] = true
			op, covered := admCovered[key]
			reason, excluded := admExcluded[key]
			switch {
			case covered && excluded:
				t.Errorf("%s is both covered and excluded", key)
			case covered:
				if _, ok := ops[op]; !ok {
					t.Errorf("%s is covered by %q, which is not in the table", key, op)
				}
			case excluded:
				if reason == "" {
					t.Errorf("%s is excluded without a reason", key)
				}
			default:
				t.Errorf("admin.%s has no operation and no exclusion: add admin/%s or list it in admExcluded with a reason", key, key)
			}
		}
	}
	// A stale entry (a method that no longer exists) is as wrong as a missing one.
	for _, m := range []map[string]string{admCovered, admExcluded} {
		for key := range m {
			if !seen[key] {
				t.Errorf("%s is listed but admin has no such method", key)
			}
		}
	}
}

// Every admin/* operation belongs to a method of admin (or to the documented
// extras), so the table cannot grow an operation the parity map does not know.
func TestAdminParityEveryOperationIsAccountedFor(t *testing.T) {
	known := map[string]bool{"admin/sessions/export_file_name": true} // admin.ExportFileName, a package function
	for _, op := range admCovered {
		known[op] = true
	}
	var unknown []string
	for m := range opIndex() {
		if len(m) > 6 && m[:6] == "admin/" && !known[m] {
			unknown = append(unknown, m)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("operations with no admin method behind them in admCovered: %v", unknown)
	}
}
