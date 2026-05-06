// stado_http_upload_* — chunked HTTP request body delivery for plugins
// uploading large payloads. EP-0038i (companion to v0.38h response
// streaming).
//
// stado_http_request and _request_stream both buffer the request body
// in wasm memory (via args.body_b64) before sending. That OOMs the
// wasm instance for multi-GB uploads. The upload variant gives the
// plugin a writer-handle: open → write chunks → finish → drain
// response. The body crosses the boundary chunk-by-chunk, never
// buffered whole.
//
// Three imports:
//
//   stado_http_upload_create(args_json, out, out_max) → i32
//     Opens an upload. Returns JSON `{"upload_handle": <u32>}` on
//     success (positive byte count). args: method/url/headers/
//     timeout_ms/content_length. Body is NOT in args.
//
//   stado_http_upload_write(upload_handle, data, data_len) → i32
//     Writes one chunk to the request body. Returns bytes written
//     or -1 on error / unknown handle. Plugin loops calling this
//     until the body is fully delivered.
//
//   stado_http_upload_finish(upload_handle, out, out_max) → i32
//     Closes the body writer, waits for the in-flight request to
//     return, writes the response JSON `{status, headers,
//     body_handle}` to out, frees the upload handle. The returned
//     body_handle is a httpresp:<id> the plugin drains via
//     stado_http_response_read/_response_close.
//
// Handle type: HandleTypeHTTPUpload ("httpup"). Per-Runtime cap:
// maxHTTPUploadsPerRuntime (8). Reaper: closeAllHTTPUploads.
//
// Capability: reuses net:http_request[:<host>]. No new cap surface.
//
// Out of scope: HTTP/2 server-push body, multipart streaming, request
// trailers, full bidirectional duplex (the existing pattern of
// "upload all then drain response" composes from upload_finish +
// response_read; truly concurrent upload-while-downloading is rare
// and worth its own design).
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const maxHTTPUploadsPerRuntime = 8

// uploadStream is the host-side state for an in-flight upload.
//
// The pipe writer is what the plugin's stado_http_upload_write feeds.
// A goroutine launched at create-time runs client.Do(req) reading from
// the pipe; the response (and any dial-time error) lands in `respCh`.
// stado_http_upload_finish closes the writer to signal EOF, then waits
// on respCh.
type uploadStream struct {
	writer *io.PipeWriter
	respCh chan uploadDoneResult
	closed bool
}

type uploadDoneResult struct {
	resp *http.Response
	err  error
}

// uploadCreateArgs is the narrowed args shape stado_http_upload_create
// accepts. Body is delivered via _upload_write; do not include body_b64.
type uploadCreateArgs struct {
	Method        string            `json:"method"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	TimeoutMs     int               `json:"timeout_ms,omitempty"`
	ContentLength int64             `json:"content_length,omitempty"`
}

// uploadCreateResult is the JSON returned by stado_http_upload_create.
// `upload_handle` is the i32-as-uint32 handle ID; the plugin pumps
// data through it via _upload_write.
type uploadCreateResult struct {
	UploadHandle uint32 `json:"upload_handle"`
}

func registerHTTPUploadImports(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	registerHTTPUploadCreateImport(builder, host, rt)
	registerHTTPUploadWriteImport(builder, host, rt)
	registerHTTPUploadFinishImport(builder, host, rt)
}

// stado_http_upload_create(args_ptr, args_len, out_ptr, out_max) → i32
func registerHTTPUploadCreateImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module,
			argsPtr, argsLen, outPtr, outMax int32,
		) int32 {
			if !host.NetHTTPRequest && len(host.NetReqHost) == 0 {
				return -1
			}
			if argsLen <= 0 {
				return -1
			}
			raw, ok := mod.Memory().Read(uint32(argsPtr), uint32(argsLen))
			if !ok {
				return -1
			}
			var args uploadCreateArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return -1
			}
			if atomic.LoadInt64(&rt.httpUploadCount) >= maxHTTPUploadsPerRuntime {
				return -1
			}
			stream, err := openHTTPUpload(ctx, host, args)
			if err != nil {
				return -1
			}
			id, err := rt.handles.alloc(string(HandleTypeHTTPUpload), stream)
			if err != nil {
				_ = stream.writer.Close()
				return -1
			}
			atomic.AddInt64(&rt.httpUploadCount, 1)
			payload, err := json.Marshal(uploadCreateResult{UploadHandle: id})
			if err != nil {
				_ = stream.writer.Close()
				rt.handles.free(id)
				atomic.AddInt64(&rt.httpUploadCount, -1)
				return -1
			}
			if int32(len(payload)) > outMax {
				_ = stream.writer.Close()
				rt.handles.free(id)
				atomic.AddInt64(&rt.httpUploadCount, -1)
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), payload) {
				_ = stream.writer.Close()
				rt.handles.free(id)
				atomic.AddInt64(&rt.httpUploadCount, -1)
				return -1
			}
			return int32(len(payload))
		}).
		Export("stado_http_upload_create")
}

// stado_http_upload_write(handle, data_ptr, data_len) → i32
func registerHTTPUploadWriteImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			handle, dataPtr, dataLen int32,
		) int32 {
			stream, ok := lookupHTTPUpload(rt, uint32(handle))
			if !ok {
				return -1
			}
			if stream.closed {
				return -1
			}
			data, ok := mod.Memory().Read(uint32(dataPtr), uint32(dataLen))
			if !ok {
				return -1
			}
			n, err := stream.writer.Write(data)
			if err != nil {
				return -1
			}
			return int32(n)
		}).
		Export("stado_http_upload_write")
}

// stado_http_upload_finish(handle, out_ptr, out_max) → i32
//
// Closes the body writer, waits for the request goroutine to land,
// returns the response metadata + a body_handle for chunked drain.
// Frees the upload handle.
func registerHTTPUploadFinishImport(builder wazero.HostModuleBuilder, host *Host, rt *Runtime) {
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module,
			handle, outPtr, outMax int32,
		) int32 {
			stream, ok := lookupHTTPUpload(rt, uint32(handle))
			if !ok {
				return -1
			}
			if stream.closed {
				return -1
			}
			stream.closed = true
			_ = stream.writer.Close()
			result := <-stream.respCh
			rt.handles.free(uint32(handle))
			atomic.AddInt64(&rt.httpUploadCount, -1)
			if result.err != nil || result.resp == nil {
				return -1
			}
			// Hand the response body off to the existing httpresp
			// machinery so the plugin drains it via the standard
			// stado_http_response_read / _response_close imports.
			respStream := &httpRespStream{body: result.resp.Body}
			respID, err := rt.handles.alloc(string(HandleTypeHTTPResp), respStream)
			if err != nil {
				_ = result.resp.Body.Close()
				return -1
			}
			rt.httpStreamCount++
			headers := make(map[string]string, len(result.resp.Header))
			for k, vs := range result.resp.Header {
				headers[k] = strings.Join(vs, ", ")
			}
			payload, err := json.Marshal(streamRequestResult{
				Status:     result.resp.StatusCode,
				Headers:    headers,
				BodyHandle: respID,
			})
			if err != nil {
				_ = result.resp.Body.Close()
				rt.handles.free(respID)
				rt.httpStreamCount--
				return -1
			}
			if int32(len(payload)) > outMax {
				_ = result.resp.Body.Close()
				rt.handles.free(respID)
				rt.httpStreamCount--
				return -1
			}
			if !mod.Memory().Write(uint32(outPtr), payload) {
				_ = result.resp.Body.Close()
				rt.handles.free(respID)
				rt.httpStreamCount--
				return -1
			}
			return int32(len(payload))
		}).
		Export("stado_http_upload_finish")
}

// openHTTPUpload kicks off the request goroutine. The pipe reader is
// the request body; the writer goes to the plugin's _upload_write.
// Returns immediately with the upload stream; the goroutine runs
// concurrently and parks the response on stream.respCh.
func openHTTPUpload(ctx context.Context, host *Host, args uploadCreateArgs) (*uploadStream, error) {
	method, err := normalizeHTTPMethod(args.Method)
	if err != nil {
		return nil, err
	}
	if args.URL == "" {
		return nil, fmt.Errorf("http_upload_create: empty URL")
	}
	timeout := httpStreamRequestTimeout
	if args.TimeoutMs > 0 {
		t := time.Duration(args.TimeoutMs) * time.Millisecond
		if t < timeout {
			timeout = t
		}
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, method, args.URL, pr)
	if err != nil {
		_ = pw.Close()
		return nil, err
	}
	req.Header.Set("User-Agent", "stado-upload/0.1.0")
	for k, v := range args.Headers {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || isHopByHopHeader(key) {
			continue
		}
		req.Header.Set(k, v)
	}
	if args.ContentLength > 0 {
		req.ContentLength = args.ContentLength
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = httpStreamDialContext(host)
	client := &http.Client{Timeout: timeout, Transport: transport}

	respCh := make(chan uploadDoneResult, 1)
	go func() {
		resp, err := client.Do(req)
		// If client.Do failed, drain the writer so subsequent
		// upload_write calls error out instead of hanging.
		if err != nil {
			_ = pw.CloseWithError(err)
		}
		respCh <- uploadDoneResult{resp: resp, err: err}
	}()

	return &uploadStream{
		writer: pw,
		respCh: respCh,
	}, nil
}

// lookupHTTPUpload fetches the *uploadStream for a handle.
func lookupHTTPUpload(rt *Runtime, handle uint32) (*uploadStream, bool) {
	if !rt.handles.isType(handle, string(HandleTypeHTTPUpload)) {
		return nil, false
	}
	v, ok := rt.handles.get(handle)
	if !ok {
		return nil, false
	}
	stream, ok := v.(*uploadStream)
	return stream, ok
}

// closeAllHTTPUploads reaps in-flight upload streams on Runtime.Close.
// Closes the writers (goroutines drain on EOF) and clears the handle
// table. Caller doesn't read responses — the wasm side is gone.
func (r *Runtime) closeAllHTTPUploads(_ context.Context) {
	r.handles.mu.Lock()
	streams := make([]*uploadStream, 0)
	for id, e := range r.handles.entries {
		if e.typeTag != string(HandleTypeHTTPUpload) {
			continue
		}
		if s, ok := e.value.(*uploadStream); ok {
			streams = append(streams, s)
		}
		delete(r.handles.entries, id)
	}
	r.handles.mu.Unlock()
	for _, s := range streams {
		if !s.closed {
			s.closed = true
			_ = s.writer.Close()
			// Drain the response channel so the goroutine can
			// finish; we don't care about the result.
			go func(s *uploadStream) { <-s.respCh }(s)
		}
	}
}
