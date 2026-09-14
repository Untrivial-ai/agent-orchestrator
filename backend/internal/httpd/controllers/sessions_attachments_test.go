package controllers

import (
	"encoding/base64"
	"path"
	"strings"
	"testing"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestDecodeSpawnAttachments(t *testing.T) {
	t.Run("nil when empty", func(t *testing.T) {
		out, err := decodeSpawnAttachments(nil)
		if err != nil || out != nil {
			t.Fatalf("want nil,nil got %v,%v", out, err)
		}
	})

	t.Run("decodes and maps extension", func(t *testing.T) {
		out, err := decodeSpawnAttachments([]AttachmentInput{
			{Name: "original.png", MimeType: "image/png", Data: b64([]byte("pngbytes"))},
			{MimeType: "IMAGE/JPEG", Data: b64([]byte("jpgbytes"))},
		})
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
		if len(out) != 2 {
			t.Fatalf("want 2 attachments got %d", len(out))
		}
		if out[0].Ext != ".png" || string(out[0].Data) != "pngbytes" {
			t.Errorf("attachment 0 = %q %q", out[0].Ext, out[0].Data)
		}
		if out[0].Name != "original.png" {
			t.Errorf("attachment 0 name = %q, want original.png", out[0].Name)
		}
		if out[1].Ext != ".jpg" {
			t.Errorf("case-insensitive mime not mapped: %q", out[1].Ext)
		}
	})

	t.Run("sanitizes an untrusted display name without retaining a path", func(t *testing.T) {
		out, err := decodeSpawnAttachments([]AttachmentInput{
			{Name: `..\\folder/Quarterly report.PNG`, MimeType: "image/png", Data: b64([]byte("pngbytes"))},
		})
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
		if got := out[0].Name; got != "Quarterly-report.png" {
			t.Fatalf("sanitized name = %q, want Quarterly-report.png", got)
		}
	})

	t.Run("rejects too many", func(t *testing.T) {
		in := make([]AttachmentInput, maxAttachments+1)
		for i := range in {
			in[i] = AttachmentInput{MimeType: "image/png", Data: b64([]byte("x"))}
		}
		_, err := decodeSpawnAttachments(in)
		if err == nil || err.code != "TOO_MANY_ATTACHMENTS" {
			t.Fatalf("want TOO_MANY_ATTACHMENTS got %v", err)
		}
	})

	t.Run("accepts non-image types", func(t *testing.T) {
		out, err := decodeSpawnAttachments([]AttachmentInput{
			{MimeType: "application/pdf", Data: b64([]byte("pdfbytes"))},
			{MimeType: "text/plain", Data: b64([]byte("textbytes"))},
		})
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
		if len(out) != 2 {
			t.Fatalf("want 2 attachments got %d", len(out))
		}
		if out[0].Ext != ".pdf" || string(out[0].Data) != "pdfbytes" {
			t.Errorf("attachment 0 = %q %q", out[0].Ext, out[0].Data)
		}
		if out[1].Ext != ".txt" || string(out[1].Data) != "textbytes" {
			t.Errorf("attachment 1 = %q %q", out[1].Ext, out[1].Data)
		}
	})

	// SVG is XML that can carry active content; it is explicitly blocked.
	t.Run("rejects svg", func(t *testing.T) {
		_, err := decodeSpawnAttachments([]AttachmentInput{{MimeType: "image/svg+xml", Data: b64([]byte("<svg/>"))}})
		if err == nil || err.code != "UNSUPPORTED_ATTACHMENT_TYPE" {
			t.Fatalf("want UNSUPPORTED_ATTACHMENT_TYPE got %v", err)
		}
	})

	t.Run("rejects invalid base64", func(t *testing.T) {
		_, err := decodeSpawnAttachments([]AttachmentInput{{MimeType: "image/png", Data: "!!!not base64!!!"}})
		if err == nil || err.code != "INVALID_ATTACHMENT_DATA" {
			t.Fatalf("want INVALID_ATTACHMENT_DATA got %v", err)
		}
	})

	t.Run("rejects empty payload", func(t *testing.T) {
		_, err := decodeSpawnAttachments([]AttachmentInput{{MimeType: "image/png", Data: ""}})
		if err == nil || err.code != "INVALID_ATTACHMENT_DATA" {
			t.Fatalf("want INVALID_ATTACHMENT_DATA got %v", err)
		}
	})

	t.Run("rejects oversized single attachment", func(t *testing.T) {
		big := b64(make([]byte, maxAttachmentBytes+1))
		_, err := decodeSpawnAttachments([]AttachmentInput{{MimeType: "image/png", Data: big}})
		if err == nil || err.code != "ATTACHMENT_TOO_LARGE" {
			t.Fatalf("want ATTACHMENT_TOO_LARGE got %v", err)
		}
	})

	t.Run("rejects oversized total", func(t *testing.T) {
		half := b64(make([]byte, maxAttachmentBytes))
		in := []AttachmentInput{
			{MimeType: "image/png", Data: half},
			{MimeType: "image/png", Data: half},
			{MimeType: "image/png", Data: half},
		}
		_, err := decodeSpawnAttachments(in)
		if err == nil || err.code != "ATTACHMENTS_TOO_LARGE" {
			t.Fatalf("want ATTACHMENTS_TOO_LARGE got %v", err)
		}
	})

	t.Run("handles unknown mime type", func(t *testing.T) {
		out, err := decodeSpawnAttachments([]AttachmentInput{
			{MimeType: "application/octet-stream", Data: b64([]byte("bytes"))},
		})
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
		if len(out) != 1 {
			t.Fatalf("want 1 attachment got %d", len(out))
		}
		// Should have a fallback extension
		if out[0].Ext == "" {
			t.Errorf("want non-empty extension got %q", out[0].Ext)
		}
	})

	t.Run("handles mime type with suffix", func(t *testing.T) {
		out, err := decodeSpawnAttachments([]AttachmentInput{
			{MimeType: "application/vnd.api+json", Data: b64([]byte("jsonbytes"))},
		})
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
		if len(out) != 1 {
			t.Fatalf("want 1 attachment got %d", len(out))
		}
		if string(out[0].Data) != "jsonbytes" {
			t.Errorf("attachment data mismatch: %q", out[0].Data)
		}
	})
}

func TestAttachmentDisplayNamesStayPortableForUntrustedMIMESubtypes(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		wantExt  string
	}{
		{name: "slash traversal", mimeType: "application/../../outside", wantExt: ".bin"},
		{name: "backslash separator", mimeType: `application/x\\outside`, wantExt: ".bin"},
		{name: "dot traversal", mimeType: "application/x..outside", wantExt: ".bin"},
		{name: "unsafe punctuation", mimeType: "application/x:outside", wantExt: ".bin"},
		{name: "control byte", mimeType: "application/x\x00outside", wantExt: ".bin"},
		{name: "overlong subtype", mimeType: "application/" + strings.Repeat("a", 300), wantExt: ".bin"},
		{name: "known png", mimeType: "IMAGE/PNG", wantExt: ".png"},
		{name: "known jpeg", mimeType: "image/jpeg", wantExt: ".jpg"},
		{name: "known text with parameter", mimeType: "text/plain; charset=utf-8", wantExt: ".txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := AttachmentInput{
				Name:     "folder/" + strings.Repeat("a", 400) + ".untrusted",
				MimeType: tt.mimeType,
				Data:     b64([]byte("payload")),
			}
			decoded, attachmentErr := decodeSpawnAttachments([]AttachmentInput{input})
			if attachmentErr != nil {
				t.Fatalf("decodeSpawnAttachments: %+v", attachmentErr)
			}
			if got := decoded[0].Ext; got != tt.wantExt {
				t.Fatalf("extension = %q, want %q", got, tt.wantExt)
			}

			native, nativeErr := conversationContent(SendConversationMessageRequest{
				Attachments: []ConversationImageContentRequest{{
					Name: input.Name, MIMEType: input.MimeType, Data: input.Data,
				}},
			})
			if nativeErr != nil {
				t.Fatalf("conversationContent: %+v", nativeErr)
			}
			for label, name := range map[string]string{
				"staged": decoded[0].Name,
				"native": native[0].Name,
			} {
				if path.Base(name) != name || strings.ContainsAny(name, `/\\`) || strings.Contains(name, "..") {
					t.Fatalf("%s display name is not a portable basename: %q", label, name)
				}
				for _, r := range name {
					if r < 0x20 || r == 0x7f || r == ':' {
						t.Fatalf("%s display name contains unsafe rune %q: %q", label, r, name)
					}
				}
				storedName := "attachment-" + strings.Repeat("a", 32) + "-" + name
				if len(storedName) > 255 {
					t.Fatalf("%s stored name length = %d, exceeds 255: %q", label, len(storedName), storedName)
				}
			}
		})
	}
}

func TestDecodeSpawnAttachmentsBlocksParameterizedSVG(t *testing.T) {
	for _, mimeType := range []string{
		"image/svg+xml; charset=utf-8",
		"image/svg+xml; =bad",
		" IMAGE/SVG+XML ; broken",
	} {
		t.Run(mimeType, func(t *testing.T) {
			input := AttachmentInput{
				Name: "vector.svg", MimeType: mimeType, Data: b64([]byte("<svg/>")),
			}
			_, err := decodeSpawnAttachments([]AttachmentInput{input})
			if err == nil || err.code != "UNSUPPORTED_ATTACHMENT_TYPE" {
				t.Fatalf("spawn: want UNSUPPORTED_ATTACHMENT_TYPE, got %+v", err)
			}
			_, err = conversationContent(SendConversationMessageRequest{
				Attachments: []ConversationImageContentRequest{{
					Name: input.Name, MIMEType: input.MimeType, Data: input.Data,
				}},
			})
			if err == nil || err.code != "UNSUPPORTED_ATTACHMENT_TYPE" {
				t.Fatalf("conversation: want UNSUPPORTED_ATTACHMENT_TYPE, got %+v", err)
			}
		})
	}
}

func TestDecodeSpawnAttachmentsTrimsWhitespace(t *testing.T) {
	out, err := decodeSpawnAttachments([]AttachmentInput{
		{MimeType: "  image/png  ", Data: "  " + b64([]byte("hi")) + "  "},
	})
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if len(out) != 1 || string(out[0].Data) != "hi" {
		t.Fatalf("whitespace not trimmed: %+v", out)
	}
}
