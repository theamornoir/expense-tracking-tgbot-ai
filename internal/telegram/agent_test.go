package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/theamornoir/expense-tracking-bot/internal/config"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
	"github.com/theamornoir/expense-tracking-bot/internal/llm"
	"github.com/theamornoir/expense-tracking-bot/internal/repository"
	"github.com/theamornoir/expense-tracking-bot/internal/service"
)

// fakeLLM is a fake LLM client for testing the tool dispatcher.
type fakeLLM struct {
	responses []*llm.Choice
	callIdx   int
	captured  []llm.ToolDefinition
}

func (f *fakeLLM) Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition) (*llm.Choice, error) {
	f.captured = tools
	if f.callIdx >= len(f.responses) {
		return &llm.Choice{Message: llm.Message{Role: "assistant", Content: "готово"}}, nil
	}
	resp := f.responses[f.callIdx]
	f.callIdx++
	return resp, nil
}

func testConfig() *config.Config {
	return &config.Config{
		AllowedUserIDs: []int64{111, 222},
		AppTimezone:    time.UTC,
	}
}

func TestAgentToolDispatcher_AddExpense(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "add_expense", Arguments: `{"category":"еда","amount":350}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Сохранил кофе 350 руб. в категории еда на сегодня."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "кофе 350")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if !contains(reply, "350") || !contains(reply, "еда") {
		t.Fatalf("unexpected reply: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatalf("ListExpenses: %v", err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("expected 1 expense, got %d", len(expensesResult.Expenses))
	}
	if expensesResult.Expenses[0].UserID != 111 {
		t.Fatalf("expected user_id 111, got %d", expensesResult.Expenses[0].UserID)
	}
	if expensesResult.Expenses[0].Amount != 35000 {
		t.Fatalf("expected 35000 cents, got %d", expensesResult.Expenses[0].Amount)
	}
}

func TestAgentToolDispatcher_UserIsolation(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	_, err := svc.AddExpense(context.Background(), 111, service.AddExpenseParams{
		Category: "еда", Amount: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_del",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "delete_expense", Arguments: `{"id":1}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Не удалось удалить: трата не найдена."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 222, "удали первую трату")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("user 111 expense should not be deleted by user 222, got %d", len(expensesResult.Expenses))
	}
	if !contains(reply, "не найдена") && !contains(reply, "не удалось") {
		t.Fatalf("expected error message about not found, got: %s", reply)
	}
}

func TestAgentToolDispatcher_InvalidCategoryGoesBackToLLM(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "add_expense", Arguments: `{"category":"котики","amount":100}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Категория 'котики' недопустима. Используйте одну из: еда, транспорт и т.д."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "котики 100")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "недопустима") {
		t.Fatalf("expected error about invalid category in reply, got: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 0 {
		t.Fatalf("expected 0 expenses after invalid category, got %d", len(expensesResult.Expenses))
	}
}

func TestAgentToolDefinitions_NoUserID(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	fake := &fakeLLM{}
	agent := NewAgent(svc, fake, testConfig())

	agent.Process(context.Background(), 111, "привет")

	for _, td := range fake.captured {
		params, ok := td.Function.Parameters.(map[string]any)
		if !ok {
			continue
		}
		props, ok := params["properties"].(map[string]any)
		if !ok {
			continue
		}
		if _, exists := props["user_id"]; exists {
			t.Fatalf("tool %s should NOT have user_id parameter", td.Function.Name)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Scenario tests derived from the audit findings.

func TestAgentScenario_TaxiYesterday(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "add_expense", Arguments: `{"category":"транспорт","amount":1200,"spent_at":"` + yesterday + `"}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Сохранил такси 1200 руб. в категории транспорт на вчера."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "такси 1200 вчера")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "1200") || !contains(reply, "транспорт") {
		t.Fatalf("unexpected reply: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("expected 1 expense, got %d", len(expensesResult.Expenses))
	}
	if expensesResult.Expenses[0].Amount != 120000 {
		t.Fatalf("expected 120000 cents, got %d", expensesResult.Expenses[0].Amount)
	}
}

func TestAgentScenario_ListWeek(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "list_expenses", Arguments: `{"limit":50,"sort":"spent"}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "За неделю вы потратили 3 траты на общую сумму 1500 руб."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "покажи траты за неделю")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "неделю") {
		t.Fatalf("unexpected reply: %s", reply)
	}
}

func TestAgentScenario_LastWasNot350But450(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	// Seed the expense directly so it is the "last" one created.
	_, err := svc.AddExpense(context.Background(), 111, service.AddExpenseParams{Category: "еда", Amount: 350})
	if err != nil {
		t.Fatal(err)
	}

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_list",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "list_expenses", Arguments: `{"sort":"created","limit":1}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_upd",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "update_expense", Arguments: `{"id":1,"amount":450}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Исправил последнюю трату на 450 руб."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "последнее было не 350, а 450")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "450") {
		t.Fatalf("unexpected reply: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("expected 1 expense, got %d", len(expensesResult.Expenses))
	}
	if expensesResult.Expenses[0].Amount != 45000 {
		t.Fatalf("expected 45000 cents, got %d", expensesResult.Expenses[0].Amount)
	}
}

func TestAgentScenario_DeleteTaxiMultiple(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	// Two taxi rides on the same day.
	svc.AddExpense(context.Background(), 111, service.AddExpenseParams{Category: "транспорт", Amount: 500})
	svc.AddExpense(context.Background(), 111, service.AddExpenseParams{Category: "транспорт", Amount: 700})

	// The LLM cannot decide which one to delete, so it asks for clarification.
	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_list",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "list_expenses", Arguments: `{"category":"транспорт","limit":50,"sort":"spent"}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "У вас две поездки на такси: 500 руб. и 700 руб. Какую именно удалить?"},
		},
	}
	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "удали такси")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "две") || !contains(reply, "Какую") {
		t.Fatalf("expected clarifying question, got: %s", reply)
	}

	// Both taxi rides must remain untouched.
	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 2 {
		t.Fatalf("expected 2 expenses (not deleted), got %d", len(expensesResult.Expenses))
	}
}

func TestAgentScenario_BreadEightyNineFifty(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "add_expense", Arguments: `{"category":"еда","amount":89.50}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Сохранил хлеб 89.5 руб. в категории еда."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "хлеб 89.50")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "89.5") || !contains(reply, "еда") {
		t.Fatalf("unexpected reply: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("expected 1 expense, got %d", len(expensesResult.Expenses))
	}
	if expensesResult.Expenses[0].Amount != 8950 {
		t.Fatalf("expected 8950 cents, got %d", expensesResult.Expenses[0].Amount)
	}
}

func TestAgentScenario_ZeroAmount(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "add_expense", Arguments: `{"category":"еда","amount":0}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Не удалось сохранить: сумма должна быть больше нуля."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "кофе 0")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "нуль") && !contains(reply, "0") && !contains(reply, "больше") {
		t.Fatalf("expected validation error, got: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 0 {
		t.Fatalf("expected 0 expenses (not created), got %d", len(expensesResult.Expenses))
	}
}

func TestAgentScenario_DeleteAll(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	svc.AddExpense(context.Background(), 111, service.AddExpenseParams{Category: "еда", Amount: 100})

	// "удали все" — бот не поддерживает массовое удаление
	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "Я могу удалить только конкретную трату по ID. Укажите, какую именно."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	reply, err := agent.Process(context.Background(), 111, "удали все")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !contains(reply, "конкретную") {
		t.Fatalf("expected refusal for mass delete, got: %s", reply)
	}

	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("expected 1 expense (not deleted), got %d", len(expensesResult.Expenses))
	}
}

func TestAgentScenario_StrangersExpenses(t *testing.T) {
	repo := newFakeRepoTelegram(testConfig())
	svc := service.New(repo, testConfig())

	// An expense owned by user 111.
	_, err := svc.AddExpense(context.Background(), 111, service.AddExpenseParams{Category: "еда", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}

	// User 222 tries to read another user's expenses.
	calls := []*llm.Choice{
		{
			Message: llm.Message{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{
				{
					ID:   "call_list",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "list_expenses", Arguments: `{}`},
				},
			}},
		},
		{
			Message: llm.Message{Role: "assistant", Content: "Трат не найдено."},
		},
	}

	fake := &fakeLLM{responses: calls}
	agent := NewAgent(svc, fake, testConfig())

	_, err = agent.Process(context.Background(), 222, "покажи траты Пети")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// User 111's expense must remain untouched.
	expensesResult, err := svc.ListExpenses(context.Background(), 111, service.ListExpensesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(expensesResult.Expenses) != 1 {
		t.Fatalf("expected user 111 expense to remain, got %d", len(expensesResult.Expenses))
	}
}

func newFakeRepoTelegram(cfg *config.Config) *fakeRepoTelegram {
	return &fakeRepoTelegram{
		items:  make(map[int64]*domain.Expense),
		nextID: 1,
		cfg:    cfg,
	}
}

type fakeRepoTelegram struct {
	mu     sync.Mutex
	items  map[int64]*domain.Expense
	nextID int64
	cfg    *config.Config
}

func (f *fakeRepoTelegram) Create(ctx context.Context, e *domain.Expense) error {
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

func (f *fakeRepoTelegram) List(ctx context.Context, userID int64, params repository.ListParams) ([]*domain.Expense, int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cfg.IsAllowed(userID) {
		return nil, 0, 0, domain.ErrNotFound
	}
	var result []*domain.Expense
	for _, e := range f.items {
		if e.UserID != userID {
			continue
		}
		result = append(result, e)
	}
	total := int64(len(result))
	return result, total, len(result), nil
}

func (f *fakeRepoTelegram) GetByID(ctx context.Context, id int64, ownerID *int64) (*domain.Expense, error) {
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

func (f *fakeRepoTelegram) Update(ctx context.Context, id int64, ownerID *int64, params repository.UpdateParams) error {
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
	e.UpdatedAt = time.Now()
	f.items[id] = e
	return nil
}

func (f *fakeRepoTelegram) Delete(ctx context.Context, id int64, ownerID *int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.items[id]
	if !ok {
		return domain.ErrNotFound
	}
	if ownerID != nil && e.UserID != *ownerID {
		return domain.ErrNotFound
	}
	delete(f.items, id)
	return nil
}
