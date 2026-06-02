package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openmule/cli2api/internal/registry"
	"github.com/openmule/cli2api/pkg/apierr"
)

const maxImageUploadBytes = 20 * 1024 * 1024 // 20 MB per file, matches mulerun studio.

// Edits handles POST /v1/images/edits. Accepts both application/json and
// multipart/form-data (OpenAI's native style).
func Edits(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		var in registry.ImageEditInput
		var perr *apiError

		switch {
		case strings.HasPrefix(ct, "multipart/form-data"):
			in, perr = parseMultipartEdit(r)
		default:
			if derr := json.NewDecoder(r.Body).Decode(&in); derr != nil {
				perr = &apiError{http.StatusBadRequest, "invalid JSON body: " + derr.Error(), "invalid_request_error"}
			}
		}
		if perr != nil {
			apierr.Write(w, apierr.StyleOpenAI, perr.status, perr.msg, perr.typ)
			return
		}

		if in.Model == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "model is required", "invalid_request_error")
			return
		}
		if in.Prompt == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "prompt is required", "invalid_request_error")
			return
		}
		if len(in.AllImages()) == 0 {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "image is required", "invalid_request_error")
			return
		}

		m, ok := registry.Get(in.Model)
		if !ok || m.MapEdit == nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusNotFound, "unknown edit model: "+in.Model, "model_not_found")
			return
		}
		body, err := m.MapEdit(in)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}

		res, err := d.Client.SubmitAndWait(r.Context(), m.VendorPath, body, d.ImageTimeout, d.PollInterval, d.PollMaxInterval)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, "upstream: "+err.Error(), "upstream_error")
			return
		}
		if res.Err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, res.Err.Error(), "vendor_error")
			return
		}

		items := make([]imageItem, 0, len(res.Images))
		for _, u := range res.Images {
			items = append(items, imageItem{URL: u})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(imageResponse{
			Created: time.Now().Unix(),
			Data:    items,
		})
	}
}

// parseMultipartEdit reads OpenAI-shape multipart: `image` (repeatable),
// `mask`, `prompt`, `model`, `n`, `size`, plus our extras.
func parseMultipartEdit(r *http.Request) (registry.ImageEditInput, *apiError) {
	// 32 MB in-memory before spilling to /tmp; per-file size is checked
	// separately when we read each file.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return registry.ImageEditInput{}, &apiError{http.StatusBadRequest, "multipart: " + err.Error(), "invalid_request_error"}
	}
	form := r.MultipartForm
	in := registry.ImageEditInput{
		Model:          formFirst(form.Value, "model"),
		Prompt:         formFirst(form.Value, "prompt"),
		Size:           formFirst(form.Value, "size"),
		AspectRatio:    formFirst(form.Value, "aspect_ratio"),
		Resolution:     formFirst(form.Value, "resolution"),
		Format:         formFirst(form.Value, "format"),
		Quality:        formFirst(form.Value, "quality"),
		NegativePrompt: formFirst(form.Value, "negative_prompt"),
	}
	if n := formFirst(form.Value, "n"); n != "" {
		if v, err := strconv.Atoi(n); err == nil {
			in.N = v
		}
	}
	// File uploads → data: URIs.
	for _, field := range []string{"image", "images[]", "images"} {
		for _, fh := range form.File[field] {
			uri, perr := fileHeaderToDataURI(fh)
			if perr != nil {
				return registry.ImageEditInput{}, perr
			}
			in.Images = append(in.Images, uri)
		}
	}
	// Mask file (single) or URL/data URI in plain text.
	if fhs, ok := form.File["mask"]; ok && len(fhs) > 0 {
		uri, perr := fileHeaderToDataURI(fhs[0])
		if perr != nil {
			return registry.ImageEditInput{}, perr
		}
		in.Mask = uri
	} else {
		in.Mask = formFirst(form.Value, "mask")
	}
	return in, nil
}

func formFirst(m map[string][]string, key string) string {
	if v, ok := m[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

func fileHeaderToDataURI(fh *multipart.FileHeader) (string, *apiError) {
	if fh.Size > maxImageUploadBytes {
		return "", &apiError{http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file %q is %d bytes; max is %d", fh.Filename, fh.Size, maxImageUploadBytes),
			"invalid_request_error"}
	}
	f, err := fh.Open()
	if err != nil {
		return "", &apiError{http.StatusBadRequest, "open uploaded file: " + err.Error(), "invalid_request_error"}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageUploadBytes+1))
	if err != nil {
		return "", &apiError{http.StatusBadRequest, "read uploaded file: " + err.Error(), "invalid_request_error"}
	}
	if len(data) > maxImageUploadBytes {
		return "", &apiError{http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file %q exceeds %d bytes", fh.Filename, maxImageUploadBytes),
			"invalid_request_error"}
	}
	ct := fh.Header.Get("Content-Type")
	// `multipart.Writer.CreateFormFile` defaults to application/octet-stream
	// even for binary uploads. Always re-sniff to get a real image/* type
	// when we can; many clients (including curl) leave the per-part header
	// empty too.
	if ct == "" || ct == "application/octet-stream" {
		ct = http.DetectContentType(data)
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
