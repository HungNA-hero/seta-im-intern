package http

import (
	"encoding/json"
	"net/http"

	"seta-im-intern/go-asset-core/internal/requestcontext"
)

type ErrorCode struct {
	Code    string
	Number  int
	Message string
}

var errorCodes = map[string]ErrorCode{
	"INTERNAL_ERROR":                {"INTERNAL_ERROR", 1000, "Internal server error, please try again"},
	"BAD_REQUEST":                   {"BAD_REQUEST", 1001, "Malformed request body or parameters"},
	"METHOD_NOT_ALLOWED":            {"METHOD_NOT_ALLOWED", 1002, "HTTP method not allowed on this endpoint"},
	"CURSOR_INVALID":                {"CURSOR_INVALID", 1003, "Pagination cursor is malformed or stale"},
	"UNAUTHENTICATED":               {"UNAUTHENTICATED", 2001, "Missing or invalid actor identity"},
	"NO_ORG_CONTEXT":                {"NO_ORG_CONTEXT", 2002, "Organization context is required"},
	"FORBIDDEN":                     {"FORBIDDEN", 2003, "The requested action is not permitted"},
	"USER_NOT_FOUND":                {"USER_NOT_FOUND", 2004, "User not found or inactive"},
	"UNKNOWN_ACTION":                {"UNKNOWN_ACTION", 2005, "Requested permission action is not recognized"},
	"TRAINER_BYPASS_DISABLED":       {"TRAINER_BYPASS_DISABLED", 2006, "Trainer bypass is disabled"},
	"TRAINER_BYPASS_EXPIRED":        {"TRAINER_BYPASS_EXPIRED", 2007, "Trainer bypass has expired"},
	"RESERVED_ROLE_CODE":            {"RESERVED_ROLE_CODE", 2008, "Role code is reserved and cannot be modified"},
	"FOLDER_NOT_FOUND":              {"FOLDER_NOT_FOUND", 3001, "Folder not found"},
	"FOLDER_ORG_MISMATCH":           {"FOLDER_ORG_MISMATCH", 3002, "Folder not found"},
	"FOLDER_NAME_CONFLICT":          {"FOLDER_NAME_CONFLICT", 3003, "A folder with this name already exists at this location"},
	"FOLDER_NOT_EMPTY":              {"FOLDER_NOT_EMPTY", 3004, "Folder contains active descendants or metadata"},
	"FOLDER_CYCLE_DETECTED":         {"FOLDER_CYCLE_DETECTED", 3005, "Move would create a cycle"},
	"FOLDER_PARENT_DELETED":         {"FOLDER_PARENT_DELETED", 3006, "Parent folder is deleted or missing"},
	"FOLDER_ROOT_PROTECTED":         {"FOLDER_ROOT_PROTECTED", 3007, "Cannot perform this action on the root folder"},
	"DELETION_PREVIEW_STALE":        {"DELETION_PREVIEW_STALE", 3008, "Folder deletion preview is stale; request a new preview"},
	"FOLDER_DELETION_IN_PROGRESS":   {"FOLDER_DELETION_IN_PROGRESS", 3009, "Folder deletion is already in progress"},
	"DELETION_JOB_NOT_FOUND":        {"DELETION_JOB_NOT_FOUND", 3010, "Folder deletion job not found"},
	"DELETION_JOB_NOT_CANCELLABLE":  {"DELETION_JOB_NOT_CANCELLABLE", 3011, "Folder deletion job cannot be cancelled or retried in its current state"},
	"FOLDER_NOT_DELETED":            {"FOLDER_NOT_DELETED", 3012, "Folder is already active"},
	"RESTORE_PARENT_DELETED":        {"RESTORE_PARENT_DELETED", 3013, "Restore the parent folder before restoring this item"},
	"LIFECYCLE_UNIT_NOT_FOUND":      {"LIFECYCLE_UNIT_NOT_FOUND", 3014, "Lifecycle unit not found"},
	"LIFECYCLE_UNIT_NOT_RESTORABLE": {"LIFECYCLE_UNIT_NOT_RESTORABLE", 3015, "Lifecycle unit cannot be restored in its current state"},
	"LIFECYCLE_JOB_NOT_FOUND":       {"LIFECYCLE_JOB_NOT_FOUND", 3016, "Lifecycle job not found"},
	"METADATA_NOT_FOUND":            {"METADATA_NOT_FOUND", 4001, "Metadata item not found"},
	"METADATA_IDENTITY_CONFLICT":    {"METADATA_IDENTITY_CONFLICT", 4002, "External identity already exists on an active item"},
	"METADATA_VALIDATION_ERROR":     {"METADATA_VALIDATION_ERROR", 4003, "Metadata field validation failed"},
	"METADATA_FOLDER_DELETED":       {"METADATA_FOLDER_DELETED", 4004, "Containing folder is deleted"},
	"METADATA_NOT_DELETED":          {"METADATA_NOT_DELETED", 4005, "Metadata item is already active"},
	"GRANT_NOT_FOUND":               {"GRANT_NOT_FOUND", 5001, "Object permission not found"},
	"GRANT_INVALID_TARGET":          {"GRANT_INVALID_TARGET", 5002, "Grant must target exactly one of user or role"},
	"MEDIA_UPLOAD_NOT_FOUND":        {"MEDIA_UPLOAD_NOT_FOUND", 6001, "Media upload not found"},
	"MEDIA_UPLOAD_IN_PROGRESS":      {"MEDIA_UPLOAD_IN_PROGRESS", 6002, "A media upload is already in progress for this asset"},
	"IDEMPOTENCY_KEY_REUSED":        {"IDEMPOTENCY_KEY_REUSED", 6003, "Idempotency key was already used with different request data"},
	"UPLOAD_SESSION_EXPIRED":        {"UPLOAD_SESSION_EXPIRED", 6004, "Upload session has expired"},
	"MEDIA_PAYLOAD_TOO_LARGE":       {"MEDIA_PAYLOAD_TOO_LARGE", 6005, "Media file exceeds the maximum allowed size"},
	"MEDIA_TYPE_UNSUPPORTED":        {"MEDIA_TYPE_UNSUPPORTED", 6006, "Media type is not supported"},
	"MEDIA_OBJECT_MISMATCH":         {"MEDIA_OBJECT_MISMATCH", 6007, "Stored object does not match the upload session"},
	"INVALID_IMAGE":                 {"INVALID_IMAGE", 6008, "File is not a valid image"},
	"IMAGE_DIMENSIONS_EXCEEDED":     {"IMAGE_DIMENSIONS_EXCEEDED", 6009, "Image dimensions exceed the maximum allowed"},
	"PROCESSING_TIMEOUT":            {"PROCESSING_TIMEOUT", 6010, "Media processing exceeded its time limit"},
	"MEDIA_STORAGE_UNAVAILABLE":     {"MEDIA_STORAGE_UNAVAILABLE", 6011, "Media storage is temporarily unavailable"},
	"MEDIA_PROCESSING_FAILED":       {"MEDIA_PROCESSING_FAILED", 6012, "Media processing failed"},
	"MEDIA_QUOTA_EXCEEDED":          {"MEDIA_QUOTA_EXCEEDED", 6013, "Organization media storage quota exceeded"},
	"MEDIA_RATE_LIMITED":            {"MEDIA_RATE_LIMITED", 6014, "Too many upload requests, please retry later"},
	"MEDIA_UPLOAD_STATE_CONFLICT":   {"MEDIA_UPLOAD_STATE_CONFLICT", 6015, "Operation is not valid for the current upload state"},
}

// internalMediaErrorCodes never reach a client. Notification isolation is an
// operator-facing state, so it is recorded and mapped to a safe public code
// rather than being added to the shared table.
var internalMediaErrorCodes = map[string]string{
	"MEDIA_NOTIFICATION_ISOLATED": "MEDIA_PROCESSING_FAILED",
}

// publicMediaErrorCode maps an internal media failure category to the safe code
// a client may see. An unrecognised category degrades to the generic media
// processing failure rather than leaking its own name.
func publicMediaErrorCode(internalCode string) string {
	if publicCode, ok := internalMediaErrorCodes[internalCode]; ok {
		return publicCode
	}
	if _, ok := errorCodes[internalCode]; ok {
		return internalCode
	}
	return "MEDIA_PROCESSING_FAILED"
}

func lookupErrorCode(code string) ErrorCode {
	if definition, ok := errorCodes[code]; ok {
		return definition
	}
	return errorCodes["INTERNAL_ERROR"]
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code string) {
	definition := lookupErrorCode(code)
	requestcontext.RecordError(r.Context(), definition.Code, definition.Number)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    definition.Code,
			"number":  definition.Number,
			"message": definition.Message,
			"traceId": requestcontext.TraceID(r.Context()),
			"service": ServiceNameAssetCore,
		},
	})
}

// writeLegacyError is a temporary classifier for handlers that still return
// legacy literals. It always delegates to writeError, the single safe-envelope
// emitter, until those handlers are migrated to explicit error codes.
func writeLegacyError(w http.ResponseWriter, r *http.Request, message string, status int) {
	code := "INTERNAL_ERROR"
	switch message {
	case "Method not allowed":
		code = "METHOD_NOT_ALLOWED"
	case "invalid internal service credential", "missing X-User-Id or X-Org-Id header":
		code = "UNAUTHENTICATED"
	case "Organization context mismatch":
		code = "FORBIDDEN"
	case "Folder not found":
		code = "FOLDER_NOT_FOUND"
	case "Invalid input":
		code = "BAD_REQUEST"
	default:
		if status == http.StatusBadRequest {
			code = "BAD_REQUEST"
		}
	}
	writeError(w, r, status, code)
}
