package transport

import "strings"

func bearerFromSubprotocol(values []string) string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "Bearer ") {
				return strings.TrimSpace(strings.TrimPrefix(part, "Bearer "))
			}
			if strings.HasPrefix(part, ACPBrowserAuthSubprotocol+".") {
				return strings.TrimPrefix(part, ACPBrowserAuthSubprotocol+".")
			}
		}
	}
	return ""
}
