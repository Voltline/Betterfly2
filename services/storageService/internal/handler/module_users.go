package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerStorageRequestModule(registerStorageUserModule)
}

func registerStorageUserModule(router *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]) {
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_UpdateUserName) (*storage.ResponseMessage, error) {
		return ctx.handler.handleUpdateUserName(ctx.request, payload.UpdateUserName)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_UpdateUserAvatar) (*storage.ResponseMessage, error) {
		return ctx.handler.handleUpdateUserAvatar(ctx.request, payload.UpdateUserAvatar)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryUser) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQueryUser(ctx.request, payload.QueryUser)
	})
}
