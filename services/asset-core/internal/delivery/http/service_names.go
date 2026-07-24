package http

// ServiceName is a stable service identifier used in logs and public error envelopes.
type ServiceName string

const (
	ServiceNameAssetCore  ServiceName = "asset-core"
	ServiceNameAccessCore ServiceName = "access-core"
)
