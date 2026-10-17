package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
	"github.com/theamornoir/expense-tracking-bot/internal/service"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// ExpenseHandler serves the REST endpoints used by the web panel.
type ExpenseHandler struct {
	service *service.ExpenseService
}

func NewExpenseHandler(svc *service.ExpenseService) *ExpenseHandler {
	return &ExpenseHandler{service: svc}
}

// Routes registers the REST routes. The caller mounts the handler under /api
// (see main), so paths here are relative: /expenses, /users.
func (h *ExpenseHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/expenses", h.listExpenses)
	r.Post("/expenses", h.createExpense)
	r.Put("/expenses/{id}", h.updateExpense)
	r.Delete("/expenses/{id}", h.deleteExpense)
	r.Get("/users", h.listUsers)
	return r
}

// listExpenses returns expenses with optional filters.
func (h *ExpenseHandler) listExpenses(w http.ResponseWriter, r *http.Request) {
	params := service.ListExpensesParams{}

	if v := r.URL.Query().Get("user_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		idPtr := &id
		params.UserID = idPtr
	}
	if v := r.URL.Query().Get("from"); v != "" {
		params.From = &v
	}
	if v := r.URL.Query().Get("to"); v != "" {
		params.To = &v
	}
	if v := r.URL.Query().Get("category"); v != "" {
		params.Category = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		params.Limit = &limit
	}
	if v := r.URL.Query().Get("sort"); v != "" {
		params.Sort = v
	}

	// ownerID nil: the head of household sees every user's expenses.
	result, err := h.service.ListExpenses(r.Context(), 0, params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type item struct {
		ID        int64   `json:"id"`
		Amount    float64 `json:"amount"` // rubles
		Category  string  `json:"category"`
		SpentAt   string  `json:"spent_at"`
		Comment   string  `json:"comment"`
		CreatedAt string  `json:"created_at"`
		UpdatedAt string  `json:"updated_at"`
	}
	expenses := make([]item, 0, len(result.Expenses))
	for _, e := range result.Expenses {
		expenses = append(expenses, item{
			ID:        e.ID,
			Amount:    service.CentsToRubles(e.Amount),
			Category:  string(e.Category),
			SpentAt:   e.SpentAt.Format("2006-01-02"),
			Comment:   e.Comment,
			CreatedAt: e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: e.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"expenses":  expenses,
		"total":     result.Total,
		"count":     result.Count,
		"truncated": result.Truncated,
	})
}

// createExpense creates a new expense from the request body.
func (h *ExpenseHandler) createExpense(w http.ResponseWriter, r *http.Request) {
	var p service.AddExpenseParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if p.UserID == nil {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if !h.service.IsAllowedUser(*p.UserID) {
		writeError(w, http.StatusBadRequest, "user not allowed")
		return
	}

	e, err := h.service.AddExpense(r.Context(), *p.UserID, p)
	if err != nil {
		h.handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         e.ID,
		"amount":     service.CentsToRubles(e.Amount),
		"category":   string(e.Category),
		"spent_at":   e.SpentAt.Format("2006-01-02"),
		"comment":    e.Comment,
		"created_at": e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"updated_at": e.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// updateExpense updates an expense by ID. ownerID nil: the head of household may edit any expense.
func (h *ExpenseHandler) updateExpense(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var p service.UpdateExpenseParams
	p.ID = id
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// ownerID nil: the head of household may edit any expense.
	e, err := h.service.UpdateExpense(r.Context(), id, nil, p)
	if err != nil {
		h.handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         e.ID,
		"amount":     service.CentsToRubles(e.Amount),
		"category":   string(e.Category),
		"spent_at":   e.SpentAt.Format("2006-01-02"),
		"comment":    e.Comment,
		"created_at": e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"updated_at": e.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// deleteExpense removes an expense by ID. ownerID nil: the head of household may delete any expense.
func (h *ExpenseHandler) deleteExpense(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := h.service.DeleteExpense(r.Context(), id, nil); err != nil {
		h.handleServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// listUsers returns the allowed user IDs for the web panel filter.
func (h *ExpenseHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	users := h.service.AllowedUserIDs()
	writeJSON(w, http.StatusOK, users)
}

func (h *ExpenseHandler) handleServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "expense not found")
	case errors.Is(err, domain.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "amount must be greater than zero")
	case errors.Is(err, domain.ErrInvalidCategory):
		writeError(w, http.StatusBadRequest, "invalid category")
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
