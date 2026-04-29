package http_server

import (
	"chatbotService/internal/chatbot"
	"context"
)

func withPrincipal(ctx context.Context, principal chatbot.BotPrincipal) context.Context {
	return context.WithValue(ctx, principalContextKey, principal)
}

func principalFromRequest(r interface{ Context() context.Context }) chatbot.BotPrincipal {
	principal, ok := r.Context().Value(principalContextKey).(chatbot.BotPrincipal)
	if !ok {
		return chatbot.BotPrincipal{}
	}
	return principal
}
