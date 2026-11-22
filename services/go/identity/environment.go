package identity

import "strings"

// FrontendOriginsFromEnvironment uses only configured origins and trusted
// deployment metadata. Preview confirmation must stay on its own deployment,
// even if a shared FRONTEND_URL still names production.
func FrontendOriginsFromEnvironment(getenv func(string) string) []string {
	var configured []string
	for _, key := range []string{"FRONTEND_URL", "CORS_ORIGIN"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			configured = append(configured, value)
		}
	}
	var deployment []string
	if getenv("VERCEL") == "1" {
		for _, key := range []string{"VERCEL_BRANCH_URL", "VERCEL_URL"} {
			if host := getenv(key); host != "" && !strings.ContainsAny(host, "/:?#@\\ \r\n\t") {
				deployment = append(deployment, "https://"+host)
			}
		}
	}
	if getenv("VERCEL_ENV") == "preview" {
		return append(deployment, configured...)
	}
	return append(configured, deployment...)
}
