package endpoints

import (
	"net/http"
	"os"
	"strings"
)

// legalDocs returns the Terms of Service and Privacy Policy links this
// instance asks new users to accept. Both are empty unless the operator sets
// them: a self-hosted instance has no terms of its own by default, while the
// hosted service points these at its published documents.
func legalDocs() (termsURL, privacyURL string) {
	return strings.TrimSpace(os.Getenv("HEADCOUNT1_TERMS_URL")),
		strings.TrimSpace(os.Getenv("HEADCOUNT1_PRIVACY_URL"))
}

// legalAcceptanceRequired reports whether sign-up must carry an explicit
// acceptance of the documents from legalDocs.
func legalAcceptanceRequired() bool {
	terms, privacy := legalDocs()
	return terms != "" || privacy != ""
}

// LegalInfo tells the register page which documents to link next to its
// acceptance checkbox. Public route; the links are public documents.
func (api *API) LegalInfo(w http.ResponseWriter, r *http.Request) {
	terms, privacy := legalDocs()
	api.respondJSON(w, http.StatusOK, map[string]string{
		"terms_url":   terms,
		"privacy_url": privacy,
	})
}
