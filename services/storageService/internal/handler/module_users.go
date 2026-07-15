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
		return ctx.handler.handleUpdateUserNameWithDB(ctx.database, ctx.request, payload.UpdateUserName, ctx.cacheKeys)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_UpdateUserAvatar) (*storage.ResponseMessage, error) {
		return ctx.handler.handleUpdateUserAvatarWithDB(ctx.database, ctx.request, payload.UpdateUserAvatar, ctx.cacheKeys)
	})
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryUser) (*storage.ResponseMessage, error) {
		return ctx.handler.handleQueryUserWithDB(ctx.database, ctx.request, payload.QueryUser)
	})
}
