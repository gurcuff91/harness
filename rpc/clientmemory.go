package rpc

import (
	"time"

	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/logx"
)

// clientMemory is a memory.Store whose data lives in the client: every method
// is a request to it (`memory/<method>`). It has no cache and no state, so it is
// safe for concurrent use; its calls CAN be concurrent: the agent runs tool
// calls in parallel, sessions open at the same time and sub-agents share the
// parent's store, so the client must serve these requests concurrently.
type clientMemory struct {
	conn    *jsonrpc.Conn
	timeout time.Duration
	logger  logx.Logger
}

var _ memory.Store = clientMemory{}

func (c clientMemory) call(method string, params, out any) error {
	return callClient(c.conn, c.logger, "memory/"+method, c.timeout, params, out)
}

func (c clientMemory) Write(cwd, slug, content string, global bool) (bool, error) {
	var res MemoryWriteResult
	if err := c.call("write", MemoryWriteParams{CWD: cwd, Slug: slug, Content: content, Global: global}, &res); err != nil {
		return false, err
	}
	return res.Created, nil
}

func (c clientMemory) Get(cwd, slug string, global bool) (memory.Memory, bool, error) {
	var res MemoryGetResult
	if err := c.call("get", MemoryKeyParams{CWD: cwd, Slug: slug, Global: global}, &res); err != nil {
		return memory.Memory{}, false, err
	}
	if !res.Found {
		return memory.Memory{}, false, nil
	}
	return res.Memory, true, nil
}

func (c clientMemory) Search(cwd, query string, includeContent bool, skip, limit int) (memory.SearchResult, error) {
	var res memory.SearchResult
	if err := c.call("search", MemorySearchParams{CWD: cwd, Query: query, IncludeContent: includeContent, Skip: skip, Limit: limit}, &res); err != nil {
		return memory.SearchResult{}, err
	}
	if res.Results == nil {
		res.Results = []memory.Memory{}
	}
	return res, nil
}

func (c clientMemory) Delete(cwd, slug string, global bool) (bool, error) {
	var res MemoryDeleteResult
	if err := c.call("delete", MemoryKeyParams{CWD: cwd, Slug: slug, Global: global}, &res); err != nil {
		return false, err
	}
	return res.Deleted, nil
}

// Close does nothing: the connection is the server's.
func (c clientMemory) Close() error { return nil }
