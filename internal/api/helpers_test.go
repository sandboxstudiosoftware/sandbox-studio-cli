package api

import (
	"encoding/json"
	"net/http"
)

// decodeJSONBody is a test helper that decodes a request body into out.
func decodeJSONBody(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}
