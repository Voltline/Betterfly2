package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerStorageRequestModule(registerStorageMessageModule)
}

func registerStorageMessageModule(router *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]) {
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_StoreNewMessage) (*storage.ResponseMessage, error) {
		return ctx.handler.handleStoreNewMessage(ctx.request, payload.StoreNewMessage)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryMessage) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQueryMessage(ctx.request, payload.QueryMessage)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QuerySyncMessages) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQuerySyncMessages(ctx.request, payload.QuerySyncMessages)
	})
}
