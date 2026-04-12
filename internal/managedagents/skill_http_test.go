package managedagents

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUploadedSkillFilesAcceptsFilesArrayField(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="files[]"; filename="demo-skill/SKILL.md"`)
	header.Set("Content-Type", "text/markdown")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write([]byte("---\nname: demo-skill\ndescription: Demo skill\n---\n")); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/skills", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c := skillUploadTestContext(req)

	_, files, err := readSkillMultipartUpload(c, true)
	if err != nil {
		t.Fatalf("readSkillMultipartUpload: %v", err)
	}
	if len(files) != 1 || files[0].Path != "demo-skill/SKILL.md" {
		t.Fatalf("files = %#v", files)
	}
}

func TestUploadedSkillFilesRejectsCombinedPayloadOverLimit(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("files", "demo-skill/SKILL.md")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), int(maxSkillUploadBytes)+1)); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/skills", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c := skillUploadTestContext(req)

	_, _, err = readSkillMultipartUpload(c, true)
	if !errors.Is(err, errSkillUploadTooLarge) {
		t.Fatalf("error = %v, want errSkillUploadTooLarge", err)
	}
}

func skillUploadTestContext(req *http.Request) *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return c
}
