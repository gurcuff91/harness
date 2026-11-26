package rpc

import "fmt"

// table is the full operation table. Each group lives in its own file so
// operations can be added without touching another group's file:
//
//	coreOps()        ops_core.go          initialize, agent/*
//	sessionOps()     ops_session.go       session/new|resume|fork|import|close
//	sessionMoreOps() ops_session_more.go  the rest of session/*
//	adminOps()       ops_admin.go         admin/<service>/<method>
func table() []Op {
	var all []Op
	all = append(all, coreOps()...)
	all = append(all, sessionOps()...)
	all = append(all, sessionMoreOps()...)
	all = append(all, adminOps()...)
	return all
}

// opIndex indexes the table by method. A duplicate method is a programming
// error and panics (a test builds the index, so it cannot ship).
func opIndex() map[string]Op {
	all := table()
	idx := make(map[string]Op, len(all))
	for _, op := range all {
		if op.Method == "" || op.call == nil {
			panic("rpc: operation without method or function")
		}
		if _, dup := idx[op.Method]; dup {
			panic(fmt.Sprintf("rpc: duplicate method %q", op.Method))
		}
		idx[op.Method] = op
	}
	return idx
}
