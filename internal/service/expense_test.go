package service

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/theamornoir/expense-tracking-bot/internal/config"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
	"github.com/theamornoir/expense-tracking-bot/internal/repository"
)

// fakeRepo is an in-memory repository used by the service unit tests.
type fakeRepo struct {
	mu     sync.Mutex
	items  map[int64]*domain.Expense
	nextID int64
	cfg    *config.Config
}

func newFakeRepo(cfg *config.Config) *fakeRepo {
	return &fakeRepo{
		items:  make(map[int64]*domain.Expense),
		nextID: 1,
		cfg:    cfg,
	}
}

func (f *fakeRepo) Create(ctx context.Context, e *domain.Expense) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cfg.IsAllowed(e.UserID) {
		return domain.ErrNotFound
	}
	e.ID = f.nextID
	f.nextID++
	e.CreatedAt = time.Now()
	e.UpdatedAt = time.Now()
	f.items[e.ID] = e
	return nil
}

func (f *fakeRepo) List(ctx context.Context, userID int64, params repository.ListParams) ([]*domain.Expense, int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !params.AllUsers && !f.cfg.IsAllowed(userID) {
		return nil, 0, 0, domain.ErrNotFound
	}

	var result []*domain.Expense
	for _, e := range f.items {
		if !params.AllUsers && e.UserID != userID {
			continue
		}
		if params.From != nil {
			from, err := time.Parse("2006-01-02", *params.From)
			if err == nil && e.SpentAt.Before(from) {
				continue
			}
		}
		if params.To != nil {
			to, err := time.Parse("2006-01-02", *params.To)
			if err == nil && e.SpentAt.After(to) {
				continue
			}
		}
		if params.Category != nil && e.Category != domain.Category(*params.Category) {
			continue
		}
		result = append(result, e)
	}

	sortExpenses(result, params.Sort)

	total := int64(len(result))
	limit := 50
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	if limit < len(result) {
		result = result[:limit]
	}
	return result, total, len(result), nil
}

func sortExpenses(items []*domain.Expense, sort string) {
	if sort == "created" {
		slices.SortFunc(items, func(a, b *domain.Expense) int {
			return cmp.Compare(b.CreatedAt.UnixNano(), a.CreatedAt.UnixNano())
		})
		return
	}
	slices.SortFunc(items, func(a, b *domain.Expense) int {
		return cmp.Compare(b.SpentAt.UnixNano(), a.SpentAt.UnixNano())
	})
}

func (f *fakeRepo) GetByID(ctx context.Context, id int64, ownerID *int64) (*domain.Expense, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if ownerID != nil && e.UserID != *ownerID {
		return nil, domain.ErrNotFound
	}
	return e, nil
}

func (f *fakeRepo) Update(ctx context.Context, id int64, ownerID *int64, params repository.UpdateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.items[id]
	if !ok {
		return domain.ErrNotFound
	}
	if ownerID != nil && e.UserID != *ownerID {
		return domain.ErrNotFound
	}
	if params.Amount != nil {
		e.Amount = *params.Amount
	}
	if params.Category != nil {
		e.Category = domain.Category(*params.Category)
	}
	if params.SpentAt != nil {
		t, err := time.Parse("2006-01-02", *params.SpentAt)
		if err != nil {
			return err
		}
		e.SpentAt = t
	}
	if params.Comment != nil {
		e.Comment = *params.Comment
	}
	e.UpdatedAt = time.Now()
	f.items[id] = e
	return nil
}

func (f *fakeRepo) Delete(ctx context.Context, id int64, ownerID *int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return domain.ErrNotFound
	}
	if ownerID != nil {
		if e, exists := f.items[id]; exists && e.UserID != *ownerID {
			return domain.ErrNotFound
		}
	}
	delete(f.items, id)
	return nil
}

// testConfig returns a two-user test configuration.
func testConfig() *config.Config {
	return &config.Config{
		AllowedUserIDs: []int64{111, 222},
		AppTimezone:    time.UTC,
	}
}

func TestAddExpense(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	e, err := svc.AddExpense(context.Background(), 111, AddExpenseParams{
		Category: "еда",
		Amount:   350.50,
		Comment:  strPtr("кофе"),
	})
	if err != nil {
		t.Fatalf("AddExpense: %v", err)
	}
	if e.ID != 1 {
		t.Fatalf("expected ID 1, got %d", e.ID)
	}
	if e.Amount != 35050 {
		t.Fatalf("expected 35050 cents, got %d", e.Amount)
	}
	if e.UserID != 111 {
		t.Fatalf("expected user 111, got %d", e.UserID)
	}
}

func TestAddExpenseInvalidAmount(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	_, err := svc.AddExpense(context.Background(), 111, AddExpenseParams{
		Category: "еда",
		Amount:   0,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
	if !errors.Is(err, domain.ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount, got %v", err)
	}
}

func TestAddExpenseInvalidCategory(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	_, err := svc.AddExpense(context.Background(), 111, AddExpenseParams{
		Category: "котики",
		Amount:   100,
	})
	if err == nil {
		t.Fatal("expected error for invalid category")
	}
	if !errors.Is(err, domain.ErrInvalidCategory) {
		t.Fatalf("expected ErrInvalidCategory, got %v", err)
	}
}

func TestUserIsolation(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	e, err := svc.AddExpense(context.Background(), 111, AddExpenseParams{
		Category: "еда",
		Amount:   100,
	})
	if err != nil {
		t.Fatalf("AddExpense: %v", err)
	}

	result, err := svc.ListExpenses(context.Background(), 222, ListExpensesParams{})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("user 222 should see 0 expenses, got %d", result.Count)
	}

	ownerID := int64(222)
	_, err = svc.UpdateExpense(context.Background(), e.ID, &ownerID, UpdateExpenseParams{
		ID: e.ID, Amount: floatPtr(999),
	})
	if err == nil {
		t.Fatal("expected error: user 222 updating user 111 expense")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	err = svc.DeleteExpense(context.Background(), e.ID, &ownerID)
	if err == nil {
		t.Fatal("expected error: user 222 deleting user 111 expense")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	owner111 := int64(111)
	updated, err := svc.UpdateExpense(context.Background(), e.ID, &owner111, UpdateExpenseParams{
		ID: e.ID, Amount: floatPtr(250),
	})
	if err != nil {
		t.Fatalf("UpdateExpense: %v", err)
	}
	if updated.Amount != 25000 {
		t.Fatalf("expected 25000 cents, got %d", updated.Amount)
	}
}

func TestListExpensesFilters(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "еда", Amount: 100, SpentAt: strPtr("2024-01-10")})
	svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "транспорт", Amount: 200, SpentAt: strPtr("2024-01-12")})
	svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "еда", Amount: 300, SpentAt: strPtr("2024-01-14")})

	result, err := svc.ListExpenses(context.Background(), 111, ListExpensesParams{Category: strPtr("еда")})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("expected 2 food expenses, got %d", result.Count)
	}
	if result.Total != 2 {
		t.Fatalf("expected total 2, got %d", result.Total)
	}

	result, err = svc.ListExpenses(context.Background(), 111, ListExpensesParams{
		From: strPtr("2024-01-11"),
		To:   strPtr("2024-01-13"),
	})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("expected 1 expense in range, got %d", result.Count)
	}

	result, err = svc.ListExpenses(context.Background(), 111, ListExpensesParams{Limit: intPtr(2)})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("expected limit 2, got %d", result.Count)
	}
	if !result.Truncated {
		t.Fatal("expected truncated=true")
	}
	if result.Total != 3 {
		t.Fatalf("expected total 3, got %d", result.Total)
	}

	result, err = svc.ListExpenses(context.Background(), 111, ListExpensesParams{Sort: "created"})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Expenses[0].Amount != 30000 {
		t.Fatalf("expected latest created first (30000), got %d", result.Expenses[0].Amount)
	}
}

func TestUpdateExpense(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	e, err := svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "еда", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}

	ownerID := int64(111)
	updated, err := svc.UpdateExpense(context.Background(), e.ID, &ownerID, UpdateExpenseParams{
		ID:       e.ID,
		Amount:   floatPtr(150.75),
		Category: strPtr("транспорт"),
		Comment:  strPtr("такси"),
	})
	if err != nil {
		t.Fatalf("UpdateExpense: %v", err)
	}
	if updated.Amount != 15075 {
		t.Fatalf("expected 15075 cents, got %d", updated.Amount)
	}
	if updated.Category != "транспорт" {
		t.Fatalf("expected transport, got %s", updated.Category)
	}
}

func TestDeleteExpense(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	e, err := svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "еда", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}

	ownerID := int64(111)
	err = svc.DeleteExpense(context.Background(), e.ID, &ownerID)
	if err != nil {
		t.Fatalf("DeleteExpense: %v", err)
	}

	// Verify the deletion by listing (the service has no GetByID).
	listAfter, _ := svc.ListExpenses(context.Background(), 111, ListExpensesParams{})
	for _, le := range listAfter.Expenses {
		if le.ID == e.ID {
			t.Fatal("expense still present after delete")
		}
	}
}

func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }

func TestListExpensesRESTPath(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	// Expenses belonging to two different users.
	svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "еда", Amount: 100, SpentAt: strPtr("2024-01-10")})
	svc.AddExpense(context.Background(), 222, AddExpenseParams{Category: "транспорт", Amount: 200, SpentAt: strPtr("2024-01-11")})

	// REST path: filtering by user_id=111 returns only user 111's expenses.
	result, err := svc.ListExpenses(context.Background(), 0, ListExpensesParams{UserID: int64Ptr(111)})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 1 || result.Expenses[0].UserID != 111 {
		t.Fatalf("expected only user 111 expenses, got count=%d ids=%v", result.Count, ids(result.Expenses))
	}

	// REST path: no user_id filter returns expenses from all users.
	result, err = svc.ListExpenses(context.Background(), 0, ListExpensesParams{})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("expected all expenses (2), got %d", result.Count)
	}

	// REST path: a non-allowed user_id without a filter is rejected.
	_, err = svc.ListExpenses(context.Background(), 999, ListExpensesParams{})
	if err == nil {
		t.Fatal("expected error for disallowed user_id without filter")
	}
}

func TestListExpensesBotPath(t *testing.T) {
	repo := newFakeRepo(testConfig())
	svc := New(repo, testConfig())

	svc.AddExpense(context.Background(), 111, AddExpenseParams{Category: "еда", Amount: 100})

	// Bot path: user 222 must not see user 111's expenses.
	result, err := svc.ListExpenses(context.Background(), 222, ListExpensesParams{})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("user 222 should see 0 expenses, got %d", result.Count)
	}

	// Bot path: user 111 sees their own expenses.
	result, err = svc.ListExpenses(context.Background(), 111, ListExpensesParams{})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("user 111 should see 1 expense, got %d", result.Count)
	}
}

// Helpers.
func int64Ptr(i int64) *int64 { return &i }

func ids(es []*domain.Expense) []int64 {
	out := make([]int64, 0, len(es))
	for _, e := range es {
		out = append(out, e.UserID)
	}
	return out
}
