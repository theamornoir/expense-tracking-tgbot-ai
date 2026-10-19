package service

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/theamornoir/expense-tracking-bot/internal/config"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
	"github.com/theamornoir/expense-tracking-bot/internal/repository"
)

const (
	defaultLimit = 50
	maxLimit     = 50
)

// ExpenseService implements the expense business logic. The same instance is used
// by both the REST API and the Telegram agent's tools.
type ExpenseService struct {
	repo repository.ExpenseRepositoryI
	loc  *time.Location
	cfg  *config.Config
}

func New(repo repository.ExpenseRepositoryI, cfg *config.Config) *ExpenseService {
	return &ExpenseService{
		repo: repo,
		loc:  cfg.AppTimezone,
		cfg:  cfg,
	}
}

// AddExpenseParams are the inputs for creating an expense (amount in rubles).
type AddExpenseParams struct {
	UserID   *int64  `json:"user_id"`
	Category string  `json:"category"`
	Amount   float64 `json:"amount"`
	SpentAt  *string `json:"spent_at"`
	Comment  *string `json:"comment"`
}

// ListExpensesParams are the filters for listing expenses.
type ListExpensesParams struct {
	UserID   *int64  `json:"user_id"`
	From     *string `json:"from"`
	To       *string `json:"to"`
	Category *string `json:"category"`
	Limit    *int    `json:"limit"`
	Sort     string  `json:"sort"`
}

// ListResult is the listing output with server-computed total and count.
type ListResult struct {
	Expenses  []*domain.Expense
	Total     int64
	Count     int
	Truncated bool
}

// UpdateExpenseParams are the fields to update (amount in rubles).
type UpdateExpenseParams struct {
	ID       int64    `json:"id"`
	Amount   *float64 `json:"amount"`
	Category *string  `json:"category"`
	SpentAt  *string  `json:"spent_at"`
	Comment  *string  `json:"comment"`
}

// DeleteExpenseParams identify the expense to delete.
type DeleteExpenseParams struct {
	ID int64 `json:"id"`
}

// AddExpense creates an expense for the user. ownerID is always injected by the server.
func (s *ExpenseService) AddExpense(ctx context.Context, userID int64, p AddExpenseParams) (*domain.Expense, error) {
	if !s.cfg.IsAllowed(userID) {
		return nil, domain.ErrNotFound
	}

	amountCents, err := rublesToCents(p.Amount)
	if err != nil {
		return nil, err
	}

	if !domain.Category(p.Category).IsValid() {
		return nil, domain.ErrInvalidCategory
	}

	spentAt := s.today()
	if p.SpentAt != nil {
		t, err := parseDate(*p.SpentAt)
		if err != nil {
			return nil, domain.ErrValidation
		}
		spentAt = t
	}

	comment := ""
	if p.Comment != nil {
		comment = *p.Comment
	}

	e := &domain.Expense{
		UserID:   userID,
		Amount:   amountCents,
		Category: domain.Category(p.Category),
		SpentAt:  spentAt,
		Comment:  comment,
	}

	if err := s.repo.Create(ctx, e); err != nil {
		return nil, fmt.Errorf("create expense: %w", err)
	}
	return e, nil
}

// ListExpenses returns the user's expenses with filters, sorting and limit.
// total is the number of records matching the filters (ignoring limit);
// count is the number of records returned.
func (s *ExpenseService) ListExpenses(ctx context.Context, userID int64, p ListExpensesParams) (*ListResult, error) {
	// REST "head of household" path: userID == 0 with no user_id filter -> all users.
	allUsers := (userID == 0 && p.UserID == nil)

	targetUserID := userID
	if p.UserID != nil {
		targetUserID = *p.UserID
	}

	if !allUsers && !s.cfg.IsAllowed(targetUserID) {
		return &ListResult{}, domain.ErrNotFound
	}

	limit := defaultLimit
	if p.Limit != nil && *p.Limit > 0 && *p.Limit <= maxLimit {
		limit = *p.Limit
	}

	sort := p.Sort
	if sort != "created" && sort != "spent" {
		sort = "spent"
	}

	params := repository.ListParams{
		AllUsers: allUsers,
		From:     p.From,
		To:       p.To,
		Category: p.Category,
		Limit:    &limit,
		Sort:     sort,
	}

	expenses, total, count, err := s.repo.List(ctx, targetUserID, params)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}

	return &ListResult{
		Expenses:  expenses,
		Total:     total,
		Count:     count,
		Truncated: total > int64(count),
	}, nil
}

// UpdateExpense updates an expense. ownerID is nil for REST (head of household),
// or &userID for the bot (per-user scoping).
func (s *ExpenseService) UpdateExpense(ctx context.Context, id int64, ownerID *int64, p UpdateExpenseParams) (*domain.Expense, error) {
	existing, err := s.repo.GetByID(ctx, id, ownerID)
	if err != nil {
		return nil, err
	}

	// Validate only the fields that were actually sent.
	if p.Amount != nil {
		amountCents, err := rublesToCents(*p.Amount)
		if err != nil {
			return nil, err
		}
		existing.Amount = amountCents
	}
	if p.Category != nil {
		if !domain.Category(*p.Category).IsValid() {
			return nil, domain.ErrInvalidCategory
		}
		existing.Category = domain.Category(*p.Category)
	}
	if p.SpentAt != nil {
		t, err := parseDate(*p.SpentAt)
		if err != nil {
			return nil, domain.ErrValidation
		}
		existing.SpentAt = t
	}
	if p.Comment != nil {
		existing.Comment = *p.Comment
	}

	params := repository.UpdateParams{
		Amount:   &existing.Amount,
		Category: strPtr(string(existing.Category)),
		SpentAt:  strPtr(existing.SpentAt.Format("2006-01-02")),
		Comment:  &existing.Comment,
	}

	if err := s.repo.Update(ctx, id, ownerID, params); err != nil {
		return nil, fmt.Errorf("update expense: %w", err)
	}

	return existing, nil
}
func (s *ExpenseService) DeleteExpense(ctx context.Context, id int64, ownerID *int64) error {
	if err := s.repo.Delete(ctx, id, ownerID); err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	return nil
}

// rublesToCents converts rubles (with kopecks) to whole kopecks without float drift.
func rublesToCents(rubles float64) (int64, error) {
	d := decimal.NewFromFloat(rubles)
	if !d.GreaterThan(decimal.Zero) {
		return 0, domain.ErrInvalidAmount
	}
	return d.Mul(decimal.NewFromInt(100)).Round(0).IntPart(), nil
}

// centsToRubles converts whole kopecks to rubles.
func CentsToRubles(cents int64) float64 {
	return float64(cents) / 100
}

// today returns the start of the current day in the app timezone.
func (s *ExpenseService) today() time.Time {
	now := time.Now().In(s.loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc)
}

// parseDate parses a YYYY-MM-DD string into midnight in the given location.
func parseDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q, expected YYYY-MM-DD", s)
	}
	return t, nil
}

func strPtr(s string) *string { return &s }

// IsAllowedUser reports whether the user may use the service.
func (s *ExpenseService) IsAllowedUser(userID int64) bool {
	return s.cfg.IsAllowed(userID)
}

// AllowedUserIDs returns a copy of the allowed user IDs (used by the web panel filter).
func (s *ExpenseService) AllowedUserIDs() []int64 {
	ids := make([]int64, len(s.cfg.AllowedUserIDs))
	copy(ids, s.cfg.AllowedUserIDs)
	return ids
}
