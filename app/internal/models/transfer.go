package models

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"penguin.local/app/internal/catalog"
)

const (
	maxTransferAttempts = 3
	maxRedirects        = 5
	maxRetryAfter       = 60 * time.Second
	maxRetryJitter      = 250 * time.Millisecond
	defaultIdleTimeout  = 30 * time.Second
	copyBufferSize      = 256 << 10
)

var (
	strongETagPattern   = regexp.MustCompile(`^"[\x21\x23-\x7e\x80-\xff]*"$`)
	contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

	errIdleTimeout    = errors.New("response stalled")
	errUnsafeRedirect = errors.New("redirect refused")
)

func newHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DisableCompression:    true,
			ForceAttemptHTTP2:     true,
			IdleConnTimeout:       60 * time.Second,
		}
	}
	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}

func checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("%w: too many redirects", errUnsafeRedirect)
	}
	if request.URL.Scheme != "https" {
		return fmt.Errorf("%w: HTTPS downgrade", errUnsafeRedirect)
	}
	return nil
}

// transferFailure pairs a user-safe error with its retry policy.
type transferFailure struct {
	err        *Error
	retry      bool
	retryAfter time.Duration
	expired    bool
}

func (f *transferFailure) Error() string { return f.err.Error() }
func (f *transferFailure) Unwrap() error { return f.err }

func permanent(code Code, artifact string, cause error) *transferFailure {
	return &transferFailure{err: newError(code, artifact, cause)}
}

func transient(code Code, artifact string, cause error) *transferFailure {
	return &transferFailure{err: newError(code, artifact, cause), retry: true}
}

func storageFailure(artifact string, err error) *transferFailure {
	return &transferFailure{err: storageError(CodeWriteFailed, artifact, err)}
}

type transferer struct {
	client *http.Client
	store  *store
	idle   time.Duration
	delay  func(attempt int, retryAfter time.Duration) time.Duration
}

// retryDelay honors a bounded Retry-After, else backs off exponentially.
func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxRetryAfter)
	}
	return time.Second<<(attempt-1) + rand.N(maxRetryJitter)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return min(time.Duration(seconds)*time.Second, maxRetryAfter)
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return min(when.Sub(now), maxRetryAfter)
	}
	return 0
}

// transferSpec is one artifact bound to its journal entry.
type transferSpec struct {
	artifact catalog.Artifact
	rel      string
	etag     func() string
	saveETag func(string) error
	progress func(int64)
	budget   *int
}

// transfer runs attempts until success, a permanent failure, or the shared
// per-artifact budget is spent. Stop causes are returned unchanged.
func (t *transferer) transfer(ctx context.Context, spec transferSpec) error {
	refreshedExpiredURL := false
	for attempt := 1; ; attempt++ {
		*spec.budget--
		err := t.attempt(ctx, spec)
		if err == nil {
			return nil
		}
		var failure *transferFailure
		if !errors.As(err, &failure) {
			return err
		}
		retry, wait := failure.retry, time.Duration(0)
		switch {
		case failure.expired && !refreshedExpiredURL:
			refreshedExpiredURL, retry = true, true
		case retry:
			wait = t.delay(attempt, failure.retryAfter)
		}
		if !retry || *spec.budget <= 0 {
			return failure.err
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (t *transferer) attempt(ctx context.Context, spec transferSpec) (err error) {
	name := spec.artifact.Path
	size := spec.artifact.Size()
	etag := spec.etag()
	file, err := t.store.open(spec.rel, os.O_RDWR|os.O_CREATE, 0o600, true)
	if err != nil {
		return storageFailure(name, err)
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		if closeErr := t.store.hooks.close(file); closeErr != nil && err == nil {
			err = storageFailure(name, closeErr)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return storageFailure(name, err)
	}
	offset := info.Size()
	if offset > size || !strongETagPattern.MatchString(etag) {
		offset = 0
	}

	requestCtx, cancelRequest := context.WithCancelCause(ctx)
	defer cancelRequest(nil)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, spec.artifact.URL, nil)
	if err != nil {
		return permanent(CodeInvalidManifest, name, err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "Penguin")
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		request.Header.Set("If-Range", etag)
	}
	response, err := t.client.Do(request)
	if err != nil {
		return classifyRequestError(ctx, err, name)
	}
	defer response.Body.Close()

	complete, err := validateResponse(response, spec.artifact, &offset, etag)
	if err != nil || complete {
		return err
	}
	if response.StatusCode == http.StatusOK {
		// Truncate before recording the new validator: a crash between the two
		// must never pair old bytes with the new representation's identity.
		if err := file.Truncate(0); err != nil {
			return storageFailure(name, err)
		}
		saved := response.Header.Get("ETag")
		if !strongETagPattern.MatchString(saved) {
			saved = ""
		}
		if err := spec.saveETag(saved); err != nil {
			return storageFailure(name, err)
		}
		spec.progress(0)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return storageFailure(name, err)
	}
	if err := t.copyBody(ctx, cancelRequest, response.Body, file, spec, offset, size-offset); err != nil {
		return err
	}
	closed = true
	if err := t.store.hooks.close(file); err != nil {
		return storageFailure(name, err)
	}
	return ctx.Err()
}

// validateResponse rejects unsafe metadata before staging bytes change. It
// reports complete when a 416 confirms the retained file is already whole.
func validateResponse(response *http.Response, artifact catalog.Artifact, offset *int64, etag string) (bool, error) {
	name := artifact.Path
	size := artifact.Size()
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return false, permanent(CodeInvalidResponse, name, errors.New("unexpected content encoding"))
	}
	switch response.StatusCode {
	case http.StatusOK:
		*offset = 0
	case http.StatusPartialContent:
		if err := validateRange(response, *offset, size, etag); err != nil {
			return false, permanent(CodeInvalidResponse, name, err)
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if *offset != size || response.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", size) {
			return false, permanent(CodeInvalidResponse, name, errors.New("unreconciled 416"))
		}
		return true, nil
	default:
		return false, classifyStatus(response, artifact)
	}
	if response.ContentLength >= 0 && response.ContentLength != size-*offset {
		return false, permanent(CodeInvalidResponse, name, errors.New("unexpected response length"))
	}
	return false, nil
}

func validateRange(response *http.Response, offset, size int64, etag string) error {
	match := contentRangePattern.FindStringSubmatch(response.Header.Get("Content-Range"))
	if offset == 0 || match == nil || response.Header.Get("ETag") != etag {
		return errors.New("invalid resume identity or range")
	}
	var bounds [3]int64
	for index := range bounds {
		value, err := strconv.ParseInt(match[index+1], 10, 64)
		if err != nil {
			return errors.New("range out of bounds")
		}
		bounds[index] = value
	}
	if bounds[0] != offset || bounds[1] != size-1 || bounds[2] != size {
		return errors.New("unexpected range bounds")
	}
	return nil
}

func classifyStatus(response *http.Response, artifact catalog.Artifact) *transferFailure {
	name := artifact.Path
	cause := fmt.Errorf("HTTP %d", response.StatusCode)
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return permanent(CodeAuthRequired, name, cause)
	case http.StatusForbidden:
		failure := permanent(CodeForbidden, name, cause)
		// A 403 reached through a redirect usually means a signed URL expired.
		failure.expired = response.Request.URL.String() != artifact.URL
		return failure
	case http.StatusNotFound, http.StatusGone:
		return permanent(CodeNotFound, name, cause)
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		failure := transient(CodeServer, name, cause)
		failure.retryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		return failure
	default:
		return permanent(CodeServer, name, cause)
	}
}

func classifyRequestError(ctx context.Context, err error, name string) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var verification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &verification), errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid):
		return permanent(CodeTLS, name, err)
	case errors.Is(err, errUnsafeRedirect):
		return permanent(CodeInvalidResponse, name, err)
	}
	return transient(CodeNetwork, name, err)
}

func (t *transferer) copyBody(ctx context.Context, cancel context.CancelCauseFunc, body io.Reader, file *os.File, spec transferSpec, offset, remaining int64) error {
	name := spec.artifact.Path
	idle := time.AfterFunc(t.idle, func() { cancel(errIdleTimeout) })
	defer idle.Stop()
	readFailure := func(err error) error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return transient(CodeNetwork, name, err)
	}

	buffer := make([]byte, copyBufferSize)
	for remaining > 0 {
		idle.Reset(t.idle)
		count, readErr := body.Read(buffer[:min(int64(len(buffer)), remaining)])
		if count > 0 {
			if err := t.store.writeAll(file, buffer[:count]); err != nil {
				return storageFailure(name, err)
			}
			offset += int64(count)
			remaining -= int64(count)
			spec.progress(offset)
		}
		if errors.Is(readErr, io.EOF) && remaining > 0 {
			return transient(CodeNetwork, name, io.ErrUnexpectedEOF)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readFailure(readErr)
		}
	}
	idle.Reset(t.idle)
	var extra [1]byte
	if _, err := io.ReadFull(body, extra[:]); !errors.Is(err, io.EOF) {
		if err == nil {
			return permanent(CodeInvalidResponse, name, errors.New("response exceeds the expected size"))
		}
		return readFailure(err)
	}
	return nil
}
