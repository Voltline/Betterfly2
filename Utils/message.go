package Utils

type RequestType int

const (
	Login RequestType = iota
	Exit
	ReqPost
	Key
	QueryUser
	InsertContact
	QueryGroup
	InsertGroup
	InsertGroupUser
	ReqFile
	APNsToken
	UpdateAvatar
)

type ResponseType int

const (
	Refuse ResponseType = iota
	Server
	ResPost
	ResFile
	Warn
	PubKey
	UserInfo
	GroupInfo
)
