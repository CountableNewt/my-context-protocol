package atidentity

import "github.com/bluesky-social/indigo/atproto/identity"

// PublicDirectory applies the same DNS/socket policy as OAuth discovery to
// incoming service-authorization issuer resolution.
func PublicDirectory() identity.Directory {
	client := publicHTTPClient()
	return &identity.BaseDirectory{PLCURL: "https://plc.directory", HTTPClient: *client, PLCClient: client, UserAgent: "MyContextProtocol/atproto"}
}
