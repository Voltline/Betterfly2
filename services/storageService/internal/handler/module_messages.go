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
		return ctx.handler.handleStoreNewMessageWithDB(ctx.database, ctx.request, payload.StoreNewMessage)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryMessage) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQueryMessageWithDB(ctx.database, ctx.request, payload.QueryMessage)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QuerySyncMessages) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQuerySyncMessagesWithDB(ctx.database, ctx.request, payload.QuerySyncMessages)
	})
}
