package admin_test

import (
	"errors"
	"testing"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

func TestSettings(t *testing.T) {
	ad := newAdmin(t)
	if v := ad.Settings.Get(); v.ThinkingLevel != "off" || v.ActiveModel != "" {
		t.Fatalf("defaults: %+v", v)
	}
	if err := ad.Settings.SetActiveModel("p/m"); err != nil {
		t.Fatal(err)
	}
	if err := ad.Settings.SetThinkingLevel("high"); err != nil {
		t.Fatal(err)
	}
	if v := ad.Settings.Get(); v.ActiveModel != "p/m" || v.ThinkingLevel != "high" {
		t.Fatalf("%+v", v)
	}
	err := ad.Settings.SetThinkingLevel("bogus")
	if !errors.Is(err, admin.ErrInvalid) || !errors.Is(err, admin.ErrUnprocessable) || !errors.Is(err, config.ErrInvalidThinkingLevel) {
		t.Fatalf("bad level must match all three: %v", err)
	}
	if ad.Settings.Get().ThinkingLevel != "high" {
		t.Fatal("a rejected level must change nothing")
	}
}

func TestMCP(t *testing.T) {
	ad := newAdmin(t)
	if err := ad.MCP.Put("s", types.MCPServer{Command: "echo", Args: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	err := ad.MCP.Put("bad", types.MCPServer{})
	if !errors.Is(err, types.ErrInvalidMCPServer) || !errors.Is(err, admin.ErrInvalid) || !errors.Is(err, admin.ErrUnprocessable) {
		t.Fatalf("invalid: %v", err)
	}
	err = ad.MCP.Put("both", types.MCPServer{Command: "a", URL: "http://x"})
	if !errors.Is(err, types.ErrInvalidMCPServer) {
		t.Fatalf("both: %v", err)
	}
	if len(ad.MCP.List()) != 1 {
		t.Fatalf("a rejected server must not be stored: %v", ad.MCP.List())
	}
	if err := ad.MCP.SetEnabled("s", false); err != nil {
		t.Fatal(err)
	}
	got, _ := ad.MCP.Get("s")
	if !got.Disabled || got.Command != "echo" || len(got.Args) != 1 {
		t.Fatalf("disable must keep the rest: %+v", got)
	}
	ad.MCP.SetEnabled("s", true)
	if got, _ := ad.MCP.Get("s"); got.Disabled {
		t.Fatal("enable")
	}
	if err := ad.MCP.SetEnabled("ghost", true); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := ad.MCP.Get("ghost"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatal("get")
	}
	if err := ad.MCP.Delete("ghost"); !errors.Is(err, admin.ErrNotFound) || err.Error() != "mcp server not found: ghost" {
		t.Fatalf("%v", err)
	}
	if err := ad.MCP.Delete("s"); err != nil || len(ad.MCP.List()) != 0 {
		t.Fatal("delete")
	}
}

func TestMemoryScopingAndErrors(t *testing.T) {
	ad := newAdmin(t, admin.WithMemory(newMemory(t)))
	if !ad.Memory.Configured() {
		t.Fatal("configured")
	}
	m, created, err := ad.Memory.Put("/proj", "note", "hello")
	if err != nil || !created || m.Content != "hello" {
		t.Fatalf("put: %+v %v %v", m, created, err)
	}
	if _, created, _ := ad.Memory.Put("/proj", "note", "hello2"); created {
		t.Fatal("second put is an update")
	}
	ad.Memory.Put(memory.GlobalCWD, "g", "global fact")

	if got, err := ad.Memory.Get(memory.GlobalCWD, "g"); err != nil || got.CWD != memory.GlobalCWD {
		t.Fatalf("global get: %+v %v", got, err)
	}
	if _, err := ad.Memory.Get("/proj", "g"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("a global memory is not visible as project one: %v", err)
	}
	all, _ := ad.Memory.Search("", "", true, 0, 10)
	if all.Total != 2 {
		t.Fatalf("all projects: %d", all.Total)
	}
	onlyGlobal, _ := ad.Memory.Search(memory.GlobalCWD, "", true, 0, 10)
	if onlyGlobal.Total != 1 {
		t.Fatalf("global only: %d", onlyGlobal.Total)
	}
	proj, _ := ad.Memory.List("/proj", false, 0, 10)
	if proj.Total != 2 || proj.Results[0].Content != "" {
		t.Fatalf("project + globals, no content: %+v", proj)
	}
	if _, _, err := ad.Memory.Put("", "s", "c"); !errors.Is(err, admin.ErrInvalid) {
		t.Fatalf("cwd required: %v", err)
	}
	if _, _, err := ad.Memory.Put("/p", " ", "c"); !errors.Is(err, admin.ErrInvalid) || err.Error() != "slug is required" {
		t.Fatalf("slug required: %v", err)
	}
	if _, _, err := ad.Memory.Put("/p", "s", "  "); !errors.Is(err, admin.ErrUnprocessable) || err.Error() != "content is required" {
		t.Fatalf("content required: %v", err)
	}
	if err := ad.Memory.Delete("/proj", "ghost"); !errors.Is(err, admin.ErrNotFound) || err.Error() != "memory not found: ghost" {
		t.Fatalf("%v", err)
	}
	if err := ad.Memory.Delete("/proj", "note"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryNotConfigured(t *testing.T) {
	ad := newAdmin(t)
	if ad.Memory.Configured() {
		t.Fatal("no store given")
	}
	r, err := ad.Memory.Search("", "q", true, 0, 10)
	if err != nil || r.Total != 0 || r.Results == nil {
		t.Fatalf("reads are empty: %+v %v", r, err)
	}
	if _, _, err := ad.Memory.Put("/p", "s", "c"); !errors.Is(err, admin.ErrNotConfigured) {
		t.Fatalf("write: %v", err)
	}
	if _, err := ad.Memory.Get("/p", "s"); !errors.Is(err, admin.ErrNotConfigured) {
		t.Fatalf("get: %v", err)
	}
	if err := ad.Memory.Delete("/p", "s"); !errors.Is(err, admin.ErrNotConfigured) {
		t.Fatalf("delete: %v", err)
	}
}

func TestSchedulesOwnerFilter(t *testing.T) {
	ad := newAdmin(t)
	st, _ := schedule.Open()
	if err := st.Set("a", "@daily", "p1", "owner1", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("b", "@daily", "p2", "owner2", ""); err != nil {
		t.Fatal(err)
	}
	all, err := ad.Schedules.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %v %v", all, err)
	}
	one, _ := ad.Schedules.List("owner2")
	if len(one) != 1 || one[0].Slug != "b" {
		t.Fatalf("owner filter: %+v", one)
	}
	none, _ := ad.Schedules.List("nobody")
	if none == nil || len(none) != 0 {
		t.Fatalf("empty non-nil: %#v", none)
	}
}
