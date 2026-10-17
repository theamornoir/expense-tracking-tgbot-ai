package domain

import (
	"database/sql"
	"slices"
	"time"
)

// Category enumerates the allowed expense categories.
type Category string

const (
	CategoryFood          Category = "еда"
	CategoryTransport     Category = "транспорт"
	CategoryHousing       Category = "жильё"
	CategoryEntertainment Category = "развлечения"
	CategoryHealth        Category = "здоровье"
	CategoryPurchases     Category = "покупки"
	CategoryOther         Category = "другое"
)

// AllCategories returns every allowed category.
func AllCategories() []Category {
	return []Category{
		CategoryFood,
		CategoryTransport,
		CategoryHousing,
		CategoryEntertainment,
		CategoryHealth,
		CategoryPurchases,
		CategoryOther,
	}
}

// IsValid reports whether the category is in the allowed list.
func (c Category) IsValid() bool {
	return slices.Contains(AllCategories(), c)
}

// Expense is the domain entity for a single expense record.
type Expense struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Amount    int64     `json:"amount"` // копейки
	Category  Category  `json:"category"`
	SpentAt   time.Time `json:"spent_at"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrNotFound is returned when the expense does not exist or is not owned by the caller.
var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "expense not found" }
func (e *notFoundError) Unwrap() error { return sql.ErrNoRows }

// ErrInvalidAmount is returned for a non-positive or malformed amount.
var ErrInvalidAmount = &errValidation{msg: "amount must be greater than zero"}

// ErrInvalidCategory is returned for a category not in the allowed list.
var ErrInvalidCategory = &errValidation{msg: "invalid category"}

type errValidation struct {
	msg string
}

func (e *errValidation) Error() string { return e.msg }

// ErrValidation is the umbrella error for input validation failures.
var ErrValidation = &errValidation{msg: "validation error"}
