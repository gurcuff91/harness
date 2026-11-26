package rpc

// adminOps are the admin/<service>/<method> operations, one file per group
// (admin_sessions.go, admin_providers.go, admin_config.go).
func adminOps() []Op {
	var ops []Op
	ops = append(ops, adminSessionsOps()...)
	ops = append(ops, adminProvidersOps()...)
	ops = append(ops, adminSettingsOps()...)
	ops = append(ops, adminMCPOps()...)
	ops = append(ops, adminMemoryOps()...)
	ops = append(ops, adminSchedulesOps()...)
	return ops
}
