package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
)

// ExpenseRepository stores expenses in PostgreSQL via pgx (plain SQL, no ORM).
type ExpenseRepository struct {
	pool *pgxpool.Pool
}

// ExpenseRepositoryI abstracts the storage layer (so tests can use an in-memory fake).
type ExpenseRepositoryI interface {
	Create(ctx context.Context, e *domain.Expense) error
	List(ctx context.Context, userID int64, params ListParams) ([]*domain.Expense, int64, int, error)
	GetByID(ctx context.Context, id int64, ownerID *int64) (*domain.Expense, error)
	Update(ctx context.Context, id int64, ownerID *int64, params UpdateParams) error
	Delete(ctx context.Context, id int64, ownerID *int64) error
}

func New(pool *pgxpool.Pool) *ExpenseRepository {
	return &ExpenseRepository{pool: pool}
}

// Create inserts a new expense and fills in the generated ID and timestamps.
func (r *ExpenseRepository) Create(ctx context.Context, e *domain.Expense) error {
	query := `
		INSERT INTO expenses (user_id, amount, category, spent_at, comment)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at
	`
	row := r.pool.QueryRow(ctx, query, e.UserID, e.Amount, e.Category, e.SpentAt, e.Comment)
	if err := row.Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return fmt.Errorf("insert expense: %w", err)
	}
	return nil
}

// List returns expenses matching the filters, sorted and limited. It also returns
// total (all rows matching the filters, ignoring the limit) and count (rows returned).
func (r *ExpenseRepository) List(ctx context.Context, userID int64, params ListParams) ([]*domain.Expense, int64, int, error) {
	var where string
	args := []any{}
	argIdx := 1

	if !params.AllUsers {
		where = fmt.Sprintf("WHERE user_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}

	if params.From != nil {
		if where != "" {
			where += " AND"
		} else {
			where = "WHERE"
		}
		where += fmt.Sprintf(" spent_at >= $%d", argIdx)
		args = append(args, *params.From)
		argIdx++
	}
	if params.To != nil {
		if where != "" {
			where += " AND"
		} else {
			where = "WHERE"
		}
		where += fmt.Sprintf(" spent_at <= $%d", argIdx)
		args = append(args, *params.To)
		argIdx++
	}
	if params.Category != nil {
		if where != "" {
			where += " AND"
		} else {
			where = "WHERE"
		}
		where += fmt.Sprintf(" category = $%d", argIdx)
		args = append(args, *params.Category)
		argIdx++
	}

	orderBy := "ORDER BY spent_at DESC, created_at DESC"
	if params.Sort == "created" {
		orderBy = "ORDER BY created_at DESC"
	}

	// Count total rows under the same filters, without LIMIT/OFFSET.
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM expenses %s", where)
	var total int64
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, 0, fmt.Errorf("count expenses: %w", err)
	}

	selectQuery := fmt.Sprintf(`
		SELECT id, user_id, amount, category, spent_at, comment, created_at, updated_at
		FROM expenses %s
		%s
	`, where, orderBy)

	if params.Limit != nil && *params.Limit > 0 {
		selectQuery += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, *params.Limit)
		argIdx++
	}

	rows, err := r.pool.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("query expenses: %w", err)
	}
	defer rows.Close()

	expenses, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[domain.Expense])
	if err != nil {
		return nil, 0, 0, fmt.Errorf("scan expenses: %w", err)
	}

	return expenses, total, len(expenses), nil
}

// GetByID returns an expense by ID. A nil ownerID means unrestricted access.
func (r *ExpenseRepository) GetByID(ctx context.Context, id int64, ownerID *int64) (*domain.Expense, error) {
	args := []any{id}
	argIdx := 2

	where := "WHERE id = $1"
	if ownerID != nil {
		where += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, *ownerID)
		argIdx++
	}

	query := fmt.Sprintf(`
		SELECT id, user_id, amount, category, spent_at, comment, created_at, updated_at
		FROM expenses
		%s
	`, where)

	row := r.pool.QueryRow(ctx, query, args...)
	var e domain.Expense
	if err := row.Scan(
		&e.ID, &e.UserID, &e.Amount, &e.Category, &e.SpentAt, &e.Comment, &e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("query expense by id: %w", err)
	}
	return &e, nil
}

// Update updates the given fields of an expense, enforcing the owner check.
func (r *ExpenseRepository) Update(ctx context.Context, id int64, ownerID *int64, params UpdateParams) error {
	setClauses := []string{}
	args := []any{id}
	argIdx := 2

	if params.Amount != nil {
		setClauses = append(setClauses, fmt.Sprintf("amount = $%d", argIdx))
		args = append(args, *params.Amount)
		argIdx++
	}
	if params.Category != nil {
		setClauses = append(setClauses, fmt.Sprintf("category = $%d", argIdx))
		args = append(args, *params.Category)
		argIdx++
	}
	if params.SpentAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("spent_at = $%d", argIdx))
		args = append(args, *params.SpentAt)
		argIdx++
	}
	if params.Comment != nil {
		setClauses = append(setClauses, fmt.Sprintf("comment = $%d", argIdx))
		args = append(args, *params.Comment)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil
	}

	setClauses = append(setClauses, fmt.Sprintf("updated_at = now()"))

	where := "WHERE id = $1"
	if ownerID != nil {
		where += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, *ownerID)
		argIdx++
	}

	query := fmt.Sprintf(`
		UPDATE expenses
		SET %s
		%s
	`, strings.Join(setClauses, ", "), where)

	result, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update expense: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes an expense by ID, enforcing the owner check.
func (r *ExpenseRepository) Delete(ctx context.Context, id int64, ownerID *int64) error {
	args := []any{id}
	argIdx := 2

	where := "WHERE id = $1"
	if ownerID != nil {
		where += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, *ownerID)
		argIdx++
	}

	query := fmt.Sprintf("DELETE FROM expenses %s", where)

	result, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListParams are the filters for listing expenses.
type ListParams struct {
	AllUsers bool
	From     *string
	To       *string
	Category *string
	Limit    *int
	Sort     string // "spent" | "created"
}

// UpdateParams are the fields to update on an expense.
type UpdateParams struct {
	Amount   *int64
	Category *string
	SpentAt  *string
	Comment  *string
}
