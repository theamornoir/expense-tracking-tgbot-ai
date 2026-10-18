package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theamornoir/expense-tracking-bot/internal/config"
	"github.com/theamornoir/expense-tracking-bot/internal/domain"
	"github.com/theamornoir/expense-tracking-bot/internal/llm"
	"github.com/theamornoir/expense-tracking-bot/internal/service"
)

const (
	maxIterations = 5
	historyLimit  = 10
)

type Agent struct {
	service       *service.ExpenseService
	llm           llm.LLMClient
	cfg           *config.Config
	mu            sync.Mutex
	history       map[int64][]llm.Message
	locks         map[int64]*userLock
	locksMu       sync.Mutex
	pendingDelete map[int64][]*domain.Expense
}

type userLock struct {
	mu sync.Mutex
	ch chan struct{}
}

func NewAgent(svc *service.ExpenseService, llmClient llm.LLMClient, cfg *config.Config) *Agent {
	return &Agent{
		service:       svc,
		llm:           llmClient,
		cfg:           cfg,
		history:       make(map[int64][]llm.Message),
		locks:         make(map[int64]*userLock),
		pendingDelete: make(map[int64][]*domain.Expense),
	}
}

func (a *Agent) getUserLock(userID int64) *userLock {
	a.locksMu.Lock()
	defer a.locksMu.Unlock()
	l, ok := a.locks[userID]
	if !ok {
		l = &userLock{ch: make(chan struct{}, 1)}
		l.ch <- struct{}{}
		a.locks[userID] = l
	}
	return l
}

func (a *Agent) Process(ctx context.Context, userID int64, text string) (string, error) {
	if userID <= 0 {
		return "Извините, произошла внутренняя ошибка. Попробуйте позже.", nil
	}
	if !a.cfg.IsAllowed(userID) {
		return "Извините, у вас нет доступа к боту.", nil
	}

	lock := a.getUserLock(userID)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	return a.processLocked(ctx, userID, text)
}

func (a *Agent) processLocked(ctx context.Context, userID int64, text string) (string, error) {
	// Handle pending delete clarification: user is selecting which expense to delete by ID.
	if pending, ok := a.pendingDelete[userID]; ok && len(pending) > 0 {
		delete(a.pendingDelete, userID)
		id, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			options := ""
			for _, e := range pending {
				options += fmt.Sprintf("ID %d — %s, %.2f руб., %s\n", e.ID, e.Category, float64(kopecksToRubles(e.Amount)), e.SpentAt.Format("2006-01-02"))
			}
			return fmt.Sprintf("Укажите ID траты для удаления:\n%s", options), nil
		}
		for _, e := range pending {
			if e.ID == id {
				ownerID := userID
				if err := a.service.DeleteExpense(ctx, id, &ownerID); err != nil {
					return fmt.Sprintf(`{"error": "%s"}`, err.Error()), nil
				}
				return fmt.Sprintf(`{"status":"ok","deleted":true,"id":%d}`, id), nil
			}
		}
		return "Трата с таким ID не найдена среди кандидатов. Уточните, какую именно удалить.", nil
	}

	a.addHistory(userID, llm.Message{Role: "user", Content: text})

	now := time.Now().In(a.cfg.AppTimezone)
	systemPrompt := buildSystemPrompt(now, a.cfg.AppTimezone)

	messages := append([]llm.Message{{Role: "system", Content: systemPrompt}}, a.getHistory(userID)...)

	var finalText string
	iterations := 0
	for i := 0; i < maxIterations; i++ {
		iterations = i + 1
		choice, err := a.llm.Chat(ctx, messages, toolDefinitions())
		if err != nil {
			slog.Error("LLM chat error", "user", userID, "iteration", iterations, "err", err)
			return "Извините, не удалось связаться с ассистентом. Попробуйте позже.", nil
		}
		toolCalls := choice.ToolCalls()
		slog.Info("agent iteration",
			"user", userID,
			"iteration", iterations,
			"finish_reason", choice.FinishReason,
			"content_len", len(choice.Message.Content),
			"tool_calls", len(toolCalls),
		)

		// Record the assistant turn before executing any tools, so the
		// tool-call trace stays in order in the conversation history.
		assistantMsg := llm.Message{Role: "assistant", Content: choice.Message.Content, ToolCalls: toolCalls}
		messages = append(messages, assistantMsg)

		if len(toolCalls) == 0 {
			finalText = choice.Message.Content
			break
		}

		for _, tc := range toolCalls {
			slog.Info("agent tool call", "user", userID, "iteration", iterations, "tool", tc.Function.Name, "args", tc.Function.Arguments)
			toolResult := a.executeTool(ctx, userID, tc)
			slog.Info("agent tool result", "user", userID, "iteration", iterations, "tool", tc.Function.Name, "result", truncate(toolResult, 200))
			toolMsg := llm.Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolMsg)
		}
	}

	if finalText == "" {
		slog.Warn("agent empty final text, fallback reply", "user", userID, "iterations", iterations)
		finalText = "Извините, не удалось обработать ваш запрос. Попробуйте сформулировать иначе."
	}

	// Strip markdown: Telegram renders it as literal asterisks.
	finalText = stripMarkdown(finalText)

	slog.Info("agent done", "user", userID, "iterations", iterations, "final_len", len(finalText))

	a.addHistory(userID, llm.Message{Role: "assistant", Content: finalText})

	return finalText, nil
}

func stripMarkdown(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "```", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

func (a *Agent) executeTool(ctx context.Context, userID int64, tc llm.ToolCall) string {
	switch tc.Function.Name {
	case "add_expense":
		return a.handleAddExpense(ctx, userID, tc.Function.Arguments)
	case "list_expenses":
		return a.handleListExpenses(ctx, userID, tc.Function.Arguments)
	case "update_expense":
		return a.handleUpdateExpense(ctx, userID, tc.Function.Arguments)
	case "delete_expense":
		return a.handleDeleteExpense(ctx, userID, tc.Function.Arguments)
	default:
		return fmt.Sprintf(`{"error": "неизвестный инструмент %q"}`, tc.Function.Name)
	}
}

func (a *Agent) handleAddExpense(ctx context.Context, userID int64, argsJSON string) string {
	var p service.AddExpenseParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return fmt.Sprintf(`{"error": "неверные аргументы add_expense: %s"}`, err.Error())
	}
	e, err := a.service.AddExpense(ctx, userID, p)
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}
	return fmt.Sprintf(`{"status":"ok","expense":{"id":%d,"amount":%d,"category":"%s","spent_at":"%s","comment":"%s"}}`,
		e.ID, kopecksToRubles(e.Amount), e.Category, e.SpentAt.Format("2006-01-02"), e.Comment)
}

func (a *Agent) handleListExpenses(ctx context.Context, userID int64, argsJSON string) string {
	var p service.ListExpensesParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return fmt.Sprintf(`{"error": "неверные аргументы list_expenses: %s"}`, err.Error())
	}
	result, err := a.service.ListExpenses(ctx, userID, p)
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}

	type item struct {
		ID       int64  `json:"id"`
		Amount   int64  `json:"amount"`
		Category string `json:"category"`
		SpentAt  string `json:"spent_at"`
		Comment  string `json:"comment"`
	}
	items := make([]item, 0, len(result.Expenses))
	for _, e := range result.Expenses {
		items = append(items, item{
			ID:       e.ID,
			Amount:   kopecksToRubles(e.Amount),
			Category: string(e.Category),
			SpentAt:  e.SpentAt.Format("2006-01-02"),
			Comment:  e.Comment,
		})
	}
	out, _ := json.Marshal(map[string]any{
		"status":    "ok",
		"expenses":  items,
		"total":     result.Total,
		"count":     result.Count,
		"truncated": result.Truncated,
	})
	return string(out)
}

func (a *Agent) handleUpdateExpense(ctx context.Context, userID int64, argsJSON string) string {
	var p service.UpdateExpenseParams
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return fmt.Sprintf(`{"error": "неверные аргументы update_expense: %s"}`, err.Error())
	}
	ownerID := userID
	e, err := a.service.UpdateExpense(ctx, p.ID, &ownerID, p)
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}
	return fmt.Sprintf(`{"status":"ok","expense":{"id":%d,"amount":%d,"category":"%s","spent_at":"%s"}}`,
		e.ID, e.Amount, e.Category, e.SpentAt.Format("2006-01-02"))
}

func (a *Agent) handleDeleteExpense(ctx context.Context, userID int64, argsJSON string) string {
	var p struct {
		ID       int64   `json:"id"`
		Category *string `json:"category"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return fmt.Sprintf(`{"error": "неверные аргументы delete_expense: %s"}`, err.Error())
	}

	// If category is provided instead of id, list candidates and ask for clarification
	if p.Category != nil && p.ID == 0 {
		params := service.ListExpensesParams{
			UserID:   &userID,
			Category: p.Category,
			Limit:    ptr(50),
			Sort:     "spent",
		}
		result, err := a.service.ListExpenses(ctx, userID, params)
		if err != nil {
			return fmt.Sprintf(`{"error": "%s"}`, err.Error())
		}
		if len(result.Expenses) == 0 {
			return `{"status":"ok","message":"нет трат в этой категории"}`
		}
		if len(result.Expenses) == 1 {
			ownerID := userID
			if err := a.service.DeleteExpense(ctx, result.Expenses[0].ID, &ownerID); err != nil {
				return fmt.Sprintf(`{"error": "%s"}`, err.Error())
			}
			return fmt.Sprintf(`{"status":"ok","deleted":true,"id":%d}`, result.Expenses[0].ID)
		}
		// Multiple candidates — ask user to clarify
		a.pendingDelete[userID] = result.Expenses
		options := ""
		for _, e := range result.Expenses {
			options += fmt.Sprintf("ID %d — %s, %.2f руб., %s\n", e.ID, e.Category, float64(kopecksToRubles(e.Amount)), e.SpentAt.Format("2006-01-02"))
		}
		return fmt.Sprintf("Найдено несколько трат в категории %s. Укажите ID для удаления:\n%s", *p.Category, options)
	}

	ownerID := userID
	if err := a.service.DeleteExpense(ctx, p.ID, &ownerID); err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}
	return `{"status":"ok","deleted":true}`
}

func (a *Agent) addHistory(userID int64, msg llm.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history[userID] = append(a.history[userID], msg)
	if len(a.history[userID]) > historyLimit*2 {
		a.history[userID] = a.history[userID][len(a.history[userID])-historyLimit*2:]
	}
}

func (a *Agent) getHistory(userID int64) []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	hist := a.history[userID]
	if len(hist) > historyLimit {
		hist = hist[len(hist)-historyLimit:]
	}
	return append([]llm.Message{}, hist...)
}

func toolDefinitions() []llm.ToolDefinition {
	defs := []llm.ToolDefinition{}

	add := llm.ToolDefinition{}
	add.Type = "function"
	add.Function.Name = "add_expense"
	add.Function.Description = "Добавить новую трату"
	add.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"category": map[string]any{
				"type":        "string",
				"enum":        []string{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"},
				"description": "Категория траты",
			},
			"amount": map[string]any{
				"type":        "number",
				"description": "Сумма в рублях (допустимы копейки, например 350.50)",
			},
			"spent_at": map[string]any{
				"type":        "string",
				"description": "Дата траты в формате YYYY-MM-DD. Если не указана — сегодня.",
			},
			"comment": map[string]any{
				"type":        "string",
				"description": "Комментарий к трате",
			},
		},
		"required": []string{"category", "amount"},
	}
	defs = append(defs, add)

	list := llm.ToolDefinition{}
	list.Type = "function"
	list.Function.Name = "list_expenses"
	list.Function.Description = "Показать список трат с фильтрами"
	list.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from": map[string]any{
				"type":        "string",
				"description": "Дата начала периода YYYY-MM-DD",
			},
			"to": map[string]any{
				"type":        "string",
				"description": "Дата конца периода YYYY-MM-DD",
			},
			"category": map[string]any{
				"type": "string",
				"enum": []string{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Максимум записей (по умолчанию 50)",
			},
			"sort": map[string]any{
				"type":        "string",
				"enum":        []string{"created", "spent"},
				"description": "Сортировка: created (по времени создания) или spent (по дате траты)",
			},
		},
	}
	defs = append(defs, list)

	upd := llm.ToolDefinition{}
	upd.Type = "function"
	upd.Function.Name = "update_expense"
	upd.Function.Description = "Изменить существующую трату"
	upd.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "integer",
				"description": "ID траты",
			},
			"amount": map[string]any{
				"type":        "number",
				"description": "Новая сумма в рублях",
			},
			"category": map[string]any{
				"type": "string",
				"enum": []string{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"},
			},
			"spent_at": map[string]any{
				"type":        "string",
				"description": "Новая дата YYYY-MM-DD",
			},
			"comment": map[string]any{
				"type":        "string",
				"description": "Новый комментарий",
			},
		},
		"required": []string{"id"},
	}
	defs = append(defs, upd)

	del := llm.ToolDefinition{}
	del.Type = "function"
	del.Function.Name = "delete_expense"
	del.Function.Description = "Удалить трату по ID или категории (если несколько — уточните ID)"
	del.Function.Parameters = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "integer",
				"description": "ID траты",
			},
			"category": map[string]any{
				"type":        "string",
				"enum":        []string{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"},
				"description": "Категория для уточнения при нескольких подходящих тратах",
			},
		},
		"required": []string{},
	}
	defs = append(defs, del)

	return defs
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func buildSystemPrompt(now time.Time, tz *time.Location) string {
	tzName := "UTC"
	if tz != nil {
		tzName = tz.String()
	}
	weekday := weekdaysRU[now.Weekday()]
	return fmt.Sprintf(`Ты — ассистент учёта семейных расходов в Telegram. Пользователь пишет тебе обычным текстом на русском.

Текущая дата: %s (%s). Часовой пояс: %s. Используй эту дату для понимания "вчера", "на этой неделе", "в этом месяце" и т.д.

Допустимые категории: еда, транспорт, жильё, развлечения, здоровье, покупки, другое. Если категорию определить нельзя — ставь "другое".

Доступные инструменты:
- add_expense — добавить трату (требуются category и amount)
- list_expenses — показать траты (фильтры: from, to, category, limit, sort)
- update_expense — изменить трату по ID
- delete_expense — удалить трату по ID

Правила:
- Сумма всегда в рублях (число, копейки допустимы).
- Даты в формате YYYY-MM-DD.
- "последнее" = последняя созданная трата пользователя (используй list_expenses с sort=created, limit=1).
- Если на удаление/изменение подходит несколько трат — переспрашивай пользователя, какую именно.
- Отвечай кратко, на русском, подтверждая что именно сохранено (сумма, категория, дата).
- Не выдумывай данные. Если чего-то не хватает — спроси у пользователя.
`, now.Format("2006-01-02"), weekday, tzName)
}

var weekdaysRU = map[time.Weekday]string{
	time.Monday:    "понедельник",
	time.Tuesday:   "вторник",
	time.Wednesday: "среда",
	time.Thursday:  "четверг",
	time.Friday:    "пятница",
	time.Saturday:  "суббота",
	time.Sunday:    "воскресенье",
}

func kopecksToRubles(kopecks int64) int64 {
	return kopecks / 100
}

func ptr[T any](v T) *T { return &v }
