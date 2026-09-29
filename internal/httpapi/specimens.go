package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) listSpecimens(w http.ResponseWriter, r *http.Request) {
	values, err := api.service.ListSpecimens(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}

func (api *API) registerSpecimen(w http.ResponseWriter, r *http.Request) {
	var request contracts.RegisterSpecimenRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.RegisterSpecimen(r.Context(), request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/specimens/"+value.ID)
	writeJSON(w, http.StatusCreated, value)
}

func (api *API) getSpecimen(w http.ResponseWriter, r *http.Request) {
	value, err := api.service.GetSpecimen(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
