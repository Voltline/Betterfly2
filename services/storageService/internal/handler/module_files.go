package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerStorageRequestModule(registerStorageFileModule)
}

func registerStorageFileModule(router *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]) {
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryFileExists) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQueryFileExistsWithDB(ctx.database, ctx.request, payload.QueryFileExists)
	})
}
