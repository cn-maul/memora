package transport

import (
	"net/http"
)

// handleSearch GET /api/search
func (m *Module) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	q := getQueryParam(r, "q")
	tag := getQueryParam(r, "tag")
	page := getQueryInt(r, "page", 0)

	var tagFilter []string
	if tag != "" {
		tagFilter = []string{tag}
	}

	results, total, err := m.handler.Search.Query(q, tagFilter, page)
	if err != nil {
		writeContractError(w, err)
		return
	}

	writeOK(w, map[string]interface{}{
		"page":  page,
		"items": results,
		"total": total,
	})
}
