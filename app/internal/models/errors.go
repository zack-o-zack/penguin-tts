package models

import (
	"errors"
	"syscall"
)

// Code is a stable, redacted failure identifier shared with the UI.
type Code string

const (
	CodeInvalidManifest     Code = "INVALID_MANIFEST"
	CodeInvalidJournal      Code = "INVALID_JOURNAL"
	CodeMissingArtifact     Code = "MISSING_ARTIFACT"
	CodeSizeMismatch        Code = "SIZE_MISMATCH"
	CodeHashMismatch        Code = "HASH_MISMATCH"
	CodeSourceChanged       Code = "SOURCE_CHANGED"
	CodeUnsafePath          Code = "UNSAFE_PATH"
	CodeLocationUnavailable Code = "LOCATION_UNAVAILABLE"
	CodeAccessDenied        Code = "ACCESS_DENIED"
	CodeNoSpace             Code = "NO_SPACE"
	CodeWriteFailed         Code = "WRITE_FAILED"
	CodePublishFailed       Code = "PUBLISH_FAILED"
	CodeNetwork             Code = "NETWORK_ERROR"
	CodeTLS                 Code = "TLS_ERROR"
	CodeAuthRequired        Code = "AUTH_REQUIRED"
	CodeForbidden           Code = "FORBIDDEN"
	CodeNotFound            Code = "NOT_FOUND"
	CodeServer              Code = "SERVER_ERROR"
	CodeInvalidResponse     Code = "INVALID_RESPONSE"
	CodeStorageLocked       Code = "STORAGE_LOCKED"
	CodeStorageUnavailable  Code = "STORAGE_UNAVAILABLE"
	CodeQueueFull           Code = "QUEUE_FULL"
	CodeInvalidState        Code = "INVALID_STATE"
	CodeNotEligible         Code = "NOT_ELIGIBLE"
	CodeUnknownBundle       Code = "UNKNOWN_BUNDLE"
	CodeUnknownJob          Code = "UNKNOWN_JOB"
	CodeUnknownInstallation Code = "UNKNOWN_INSTALLATION"
	CodeInternal            Code = "INTERNAL_ERROR"
)

var messages = map[Code]string{
	CodeInvalidManifest:     "This model no longer matches the catalog. Discard it and update Penguin.",
	CodeInvalidJournal:      "Saved progress for this job is damaged. Discard it and start again.",
	CodeMissingArtifact:     "A required file is missing. Complete the folder or choose another one.",
	CodeSizeMismatch:        "A file has the wrong size. Replace the file or try again.",
	CodeHashMismatch:        "A file failed its checksum. Replace the file or try again.",
	CodeSourceChanged:       "A file changed while it was being checked. Try again when it is no longer in use.",
	CodeUnsafePath:          "A required path is a link or special file. Use ordinary files or copy them into Penguin.",
	CodeLocationUnavailable: "The folder is unavailable. Reconnect the drive or locate the folder again.",
	CodeAccessDenied:        "Penguin does not have access to this location. Grant access and try again.",
	CodeNoSpace:             "There is not enough disk space. Free some space and try again.",
	CodeWriteFailed:         "Penguin could not write to disk. Check the disk and try again.",
	CodePublishFailed:       "The model could not be installed. Try again.",
	CodeNetwork:             "The connection failed. Check your network and try again.",
	CodeTLS:                 "The server's certificate could not be trusted.",
	CodeAuthRequired:        "The download server requires sign-in.",
	CodeForbidden:           "The download server refused this file.",
	CodeNotFound:            "The download server no longer has this file.",
	CodeServer:              "The download server had a problem. Try again later.",
	CodeInvalidResponse:     "The download server sent an invalid response. Discard the partial download and try again.",
	CodeStorageLocked:       "Another Penguin window or process is managing models. Close it and try again.",
	CodeStorageUnavailable:  "Model storage is unavailable.",
	CodeQueueFull:           "Too many jobs are waiting. Let some finish first.",
	CodeInvalidState:        "That action is not available right now.",
	CodeNotEligible:         "This model is not available for download.",
	CodeUnknownBundle:       "This model is not in the catalog.",
	CodeUnknownJob:          "This job no longer exists.",
	CodeUnknownInstallation: "This installation no longer exists.",
	CodeInternal:            "Something went wrong.",
}

// Error is safe to show users: its text comes only from the code and a known
// catalog path, never from URLs or folder listings held by the cause.
type Error struct {
	Code     Code
	Artifact string
	cause    error
}

func (e *Error) Error() string {
	message := messages[e.Code]
	if e.Artifact != "" {
		return message + " (" + e.Artifact + ")"
	}
	return message
}

func (e *Error) Unwrap() error {
	return e.cause
}

func newError(code Code, artifact string, cause error) *Error {
	return &Error{Code: code, Artifact: artifact, cause: cause}
}

// storageError classifies filesystem failures, using fallback when none of the
// actionable categories apply.
func storageError(fallback Code, artifact string, err error) *Error {
	code := fallback
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		code = CodeNoSpace
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EROFS):
		code = CodeAccessDenied
	case errors.Is(err, errUnsafeFile):
		code = CodeUnsafePath
	}
	return newError(code, artifact, err)
}

func asError(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	return newError(CodeInternal, "", err)
}
