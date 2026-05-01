package utils

import "strings"

func BuildProviderURL(baseUrl string, endpoint string) string {
	baseUrl = strings.TrimSuffix(baseUrl, "/")

	if strings.HasSuffix(baseUrl, endpoint) {
		return baseUrl
	}

	baseUrl = strings.TrimSuffix(baseUrl, "/v1")

	return baseUrl + "/v1" + endpoint
}
