package telegram

import (
	"context"
	"fmt"
	"log/slog"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/theamornoir/expense-tracking-bot/internal/config"
)

// Bot wraps the Telegram long-polling bot.
type Bot struct {
	api   *tgbotapi.BotAPI
	agent *Agent
	cfg   *config.Config
}

func NewBot(token string, agent *Agent, cfg *config.Config) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	api.Debug = false
	return &Bot{api: api, agent: agent, cfg: cfg}, nil
}

// Run starts long polling and handles updates until the context is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	slog.Info("Telegram bot started", "token_present", b.cfg.TelegramToken != "")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			b.handleUpdate(ctx, update)
		}
	}
}

const ThinkingStickerID = "CAACAgIAAxkBAAFSHTBqhEdPxpayFrvOh_6ZlwTkqEoK5wACx2YAAr4ucEjIXXOW8_Sqez0E"

func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}

	userID := update.Message.From.ID
	text := update.Message.Text

	if text == "" {
		return
	}

	slog.Info("received message", "user", userID, "text", text)

	stickerID := b.sendThinkingSticker(update.Message.Chat.ID, userID)

	reply, err := b.agent.Process(ctx, userID, text)
	if err != nil {
		slog.Error("agent process error", "user", userID, "err", err)
		reply = "Извините, произошла внутренняя ошибка. Попробуйте позже."
	}

	b.deleteThinkingSticker(update.Message.Chat.ID, stickerID, userID)

	msg := tgbotapi.NewMessage(update.Message.Chat.ID, reply)
	if _, err := b.api.Send(msg); err != nil {
		slog.Error("send message error", "user", userID, "err", err)
	}
}

// sendThinkingSticker sends the "thinking" sticker and returns its message ID
// (0 if sending failed — the flow continues without the sticker).
func (b *Bot) sendThinkingSticker(chatID int64, userID int64) int {
	sticker := tgbotapi.NewSticker(chatID, tgbotapi.FileID(ThinkingStickerID))
	sent, err := b.api.Send(sticker)
	if err != nil {
		slog.Warn("send thinking sticker error", "user", userID, "err", err)
		return 0
	}
	return sent.MessageID
}

// deleteThinkingSticker removes the "thinking" sticker before the reply.
func (b *Bot) deleteThinkingSticker(chatID int64, messageID int, userID int64) {
	if messageID == 0 {
		return
	}
	del := tgbotapi.NewDeleteMessage(chatID, messageID)
	if _, err := b.api.Request(del); err != nil {
		slog.Warn("delete thinking sticker error", "user", userID, "err", err)
	}
}
