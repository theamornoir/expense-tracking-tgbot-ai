package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theamornoir/expense-tracking-bot/internal/config"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
	"github.com/theamornoir/expense-tracking-bot/internal/repository"
	"github.com/theamornoir/expense-tracking-bot/internal/service"
)

type fakeRepo struct {
	items  map[int64]*domain.Expense
	nextID int64
}

func (f *fakeRepo) Create(ctx context.Context, e *domain.Expense) error {
	e.ID = f.nextID
	f.nextID++
	f.items[e.ID] = e
	return nil
}

func (f *fakeRepo) List(ctx context.Context, userID int64, params repository.ListParams) ([]*domain.Expense, int64, int, error) {
	return []*domain.Expense{}, 0, 0, nil
}

func (f *fakeRepo) GetByID(ctx context.Context, id int64, ownerID *int64) (*domain.Expense, error) {
	return nil, domain.ErrNotFound
}

func (f *fakeRepo) Update(ctx context.Context, id int64, ownerID *int64, params repository.UpdateParams) error {
	return domain.ErrNotFound
}

func (f *fakeRepo) Delete(ctx context.Context, id int64, ownerID *int64) error {
	return domain.ErrNotFound
}

func testHandler() http.Handler {
	cfg := &config.Config{AllowedUserIDs: []int64{111}}
	repo := &fakeRepo{items: map[int64]*domain.Expense{}, nextID: 1}
	svc := service.New(repo, cfg)
	// Mount exactly like main.go does: StripPrefix("/api") + Routes().
	return http.StripPrefix("/api", NewExpenseHandler(svc).Routes())
}

// TestRoutesMountedUnderAPI ensures the handler works when mounted under /api
// (regression test: double /api prefix used to cause 404 on every endpoint).
func TestRoutesMountedUnderAPI(t *testing.T) {
	h := testHandler()
	for _, tc := range []struct {
		method string
		target string
		want   int
	}{
		{"GET", "/api/expenses", http.StatusOK},
		{"GET", "/api/users", http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s %s: got %d, want %d", tc.method, tc.target, rec.Code, tc.want)
		}
	}
}
