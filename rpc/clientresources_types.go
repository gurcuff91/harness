package rpc

// ResourcesLoadParams are the params of resources/load, the request the binary
// sends when `config.resources` has kind "client". The result is a
// resources.Resources: {system_md, agents_md, skills: [{name, description,
// location?, content}]}.
type ResourcesLoadParams struct {
	// CWD is the working directory of the session being opened.
	CWD string `json:"cwd"`
}
