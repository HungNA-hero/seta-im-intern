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
	"INTERNAL_ERROR":             {"INTERNAL_ERROR", 1000, "Internal server error, please try again"},
	"BAD_REQUEST":                {"BAD_REQUEST", 1001, "Malformed request body or parameters"},
	"METHOD_NOT_ALLOWED":         {"METHOD_NOT_ALLOWED", 1002, "HTTP method not allowed on this endpoint"},
	"CURSOR_INVALID":             {"CURSOR_INVALID", 1003, "Pagination cursor is malformed or stale"},
	"UNAUTHENTICATED":            {"UNAUTHENTICATED", 2001, "Missing or invalid actor identity"},
	"NO_ORG_CONTEXT":             {"NO_ORG_CONTEXT", 2002, "Organization context is required"},
	"FORBIDDEN":                  {"FORBIDDEN", 2003, "The requested action is not permitted"},
	"USER_NOT_FOUND":             {"USER_NOT_FOUND", 2004, "User not found or inactive"},
	"UNKNOWN_ACTION":             {"UNKNOWN_ACTION", 2005, "Requested permission action is not recognized"},
	"TRAINER_BYPASS_DISABLED":    {"TRAINER_BYPASS_DISABLED", 2006, "Trainer bypass is disabled"},
	"TRAINER_BYPASS_EXPIRED":     {"TRAINER_BYPASS_EXPIRED", 2007, "Trainer bypass has expired"},
	"RESERVED_ROLE_CODE":         {"RESERVED_ROLE_CODE", 2008, "Role code is reserved and cannot be modified"},
	"FOLDER_NOT_FOUND":           {"FOLDER_NOT_FOUND", 3001, "Folder not found"},
	"FOLDER_ORG_MISMATCH":        {"FOLDER_ORG_MISMATCH", 3002, "Folder not found"},
	"FOLDER_NAME_CONFLICT":       {"FOLDER_NAME_CONFLICT", 3003, "A folder with this name already exists at this location"},
	"FOLDER_NOT_EMPTY":           {"FOLDER_NOT_EMPTY", 3004, "Folder contains active descendants or metadata"},
	"FOLDER_CYCLE_DETECTED":      {"FOLDER_CYCLE_DETECTED", 3005, "Move would create a cycle"},
	"FOLDER_PARENT_DELETED":      {"FOLDER_PARENT_DELETED", 3006, "Parent folder is deleted or missing"},
	"FOLDER_ROOT_PROTECTED":      {"FOLDER_ROOT_PROTECTED", 3007, "Cannot perform this action on the root folder"},
	"METADATA_NOT_FOUND":         {"METADATA_NOT_FOUND", 4001, "Metadata item not found"},
	"METADATA_IDENTITY_CONFLICT": {"METADATA_IDENTITY_CONFLICT", 4002, "External identity already exists on an active item"},
	"METADATA_VALIDATION_ERROR":  {"METADATA_VALIDATION_ERROR", 4003, "Metadata field validation failed"},
	"METADATA_FOLDER_DELETED":    {"METADATA_FOLDER_DELETED", 4004, "Containing folder is deleted"},
	"GRANT_NOT_FOUND":            {"GRANT_NOT_FOUND", 5001, "Object permission not found"},
	"GRANT_INVALID_TARGET":       {"GRANT_INVALID_TARGET", 5002, "Grant must target exactly one of user or role"},
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
			"service": "asset-core",
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
