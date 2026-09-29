package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

const maxRequestBytes = 1 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, contracts.Invalid("invalid_json", "请求体不是合法 JSON"))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, contracts.Invalid("invalid_json", "请求体只能包含一个 JSON 对象"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
