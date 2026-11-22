package main

import "testing"

func TestHostedIngressRejectsUnroutedCustomMCPPath(t *testing.T) {
	if err := validateHostedMCPPath("/mcp", true); err != nil {
		t.Fatal(err)
	}
	if err := validateHostedMCPPath("/custom/mcp", true); err == nil {
		t.Fatal("hosted custom path would be advertised without a Vercel route")
	}
	if err := validateHostedMCPPath("/custom/mcp", false); err != nil {
		t.Fatal("self-hosted custom paths must remain supported")
	}
}
