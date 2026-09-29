package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) createPlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.CreatePlanRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.CreatePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/plans/"+value.ID)
	writeJSON(w, http.StatusCreated, value)
}

func (api *API) listPlansForSpecimen(w http.ResponseWriter, r *http.Request) {
	values, err := api.service.ListPlansForSpecimen(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}

func (api *API) getPlan(w http.ResponseWriter, r *http.Request) {
	value, err := api.service.GetPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (api *API) approvePlan(w http.ResponseWriter, r *http.Request) {
	var request contracts.ApprovalRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.ApprovePlan(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
