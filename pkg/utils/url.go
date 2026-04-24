package utils

import "strings"

func BuildProviderURL(baseUrl string, endpoint string) string {
	baseUrl = strings.TrimSuffix(baseUrl, "/")
	if strings.HasSuffix(baseUrl, "/v1") {
		return baseUrl + endpoint
	}
	return baseUrl + "/v1" + endpoint
}
