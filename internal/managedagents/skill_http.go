package managedagents

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
	"go.uber.org/zap"
)

const maxSkillUploadBytes int64 = 30 * 1024 * 1024

var errSkillUploadTooLarge = errors.New("skill upload exceeds 30 MiB limit")

func (h *Handler) CreateSkill(c *gin.Context) {
	principal, credential, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	displayTitle, files, err := readValidatedSkillCreateUpload(c)
	if err != nil {
		writeSkillUploadError(c, err)
		return
	}
	skill, err := h.service.CreateSkill(c.Request.Context(), principal, credential, displayTitle, files)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := skillToCreateContract(skill)
	if err != nil {
		h.logger.Error("failed to encode create-skill response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListSkills(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListSkillsV1SkillsGetParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	items, nextPage, hasMore, err := h.service.ListSkills(c.Request.Context(), principal, intQueryValue(req.Limit), stringQueryValue(req.Page), stringQueryValue(req.Source))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listSkillsToContract(items, nextPage, hasMore)
	if err != nil {
		h.logger.Error("failed to encode list-skills response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetSkill(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	skill, err := h.service.GetSkill(c.Request.Context(), principal, c.Param("skill_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := skillToGetContract(skill)
	if err != nil {
		h.logger.Error("failed to encode get-skill response", zap.Error(err), zap.String("skill_id", c.Param("skill_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DeleteSkill(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteSkill(c.Request.Context(), principal, c.Param("skill_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedSkillToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-skill response", zap.Error(err), zap.String("skill_id", c.Param("skill_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) CreateSkillVersion(c *gin.Context) {
	principal, credential, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	files, err := readValidatedSkillVersionUpload(c)
	if err != nil {
		writeSkillUploadError(c, err)
		return
	}
	version, err := h.service.CreateSkillVersion(c.Request.Context(), principal, credential, c.Param("skill_id"), files)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := skillVersionToCreateContract(version)
	if err != nil {
		h.logger.Error("failed to encode create-skill-version response", zap.Error(err), zap.String("skill_id", c.Param("skill_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListSkillVersions(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListSkillVersionsV1SkillsSkillIdVersionsGetParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	items, nextPage, hasMore, err := h.service.ListSkillVersions(c.Request.Context(), principal, c.Param("skill_id"), intQueryValue(req.Limit), stringQueryValue(req.Page))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listSkillVersionsToContract(items, nextPage, hasMore)
	if err != nil {
		h.logger.Error("failed to encode list-skill-versions response", zap.Error(err), zap.String("skill_id", c.Param("skill_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetSkillVersion(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	version, err := h.service.GetSkillVersion(c.Request.Context(), principal, c.Param("skill_id"), c.Param("version"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := skillVersionToGetContract(version)
	if err != nil {
		h.logger.Error("failed to encode get-skill-version response", zap.Error(err), zap.String("skill_id", c.Param("skill_id")), zap.String("version", c.Param("version")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DeleteSkillVersion(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteSkillVersion(c.Request.Context(), principal, c.Param("skill_id"), c.Param("version"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedSkillVersionToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-skill-version response", zap.Error(err), zap.String("skill_id", c.Param("skill_id")), zap.String("version", c.Param("version")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func readValidatedSkillCreateUpload(c *gin.Context) (*string, []uploadedSkillFile, error) {
	displayTitle, files, err := readSkillMultipartUpload(c, true)
	if err != nil {
		return nil, nil, err
	}
	return displayTitle, files, nil
}

func readValidatedSkillVersionUpload(c *gin.Context) ([]uploadedSkillFile, error) {
	_, files, err := readSkillMultipartUpload(c, false)
	return files, err
}

func readSkillMultipartUpload(c *gin.Context, allowDisplayTitle bool) (*string, []uploadedSkillFile, error) {
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return nil, nil, err
	}
	var displayTitleValues []string
	files := make([]uploadedSkillFile, 0)
	var totalBytes int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		fieldName, fileName, err := skillMultipartPartDisposition(part)
		if err != nil {
			_ = part.Close()
			return nil, nil, err
		}
		switch fieldName {
		case "display_title":
			if !allowDisplayTitle || fileName != "" {
				_ = part.Close()
				return nil, nil, errors.New("invalid multipart field: display_title")
			}
			content, readErr := io.ReadAll(part)
			_ = part.Close()
			if readErr != nil {
				return nil, nil, readErr
			}
			displayTitleValues = append(displayTitleValues, string(content))
		case "files", "files[]":
			if strings.TrimSpace(fileName) == "" {
				_ = part.Close()
				return nil, nil, errors.New("uploaded file path is required")
			}
			remaining := maxSkillUploadBytes - totalBytes
			content, readErr := io.ReadAll(io.LimitReader(part, remaining+1))
			_ = part.Close()
			if readErr != nil {
				return nil, nil, readErr
			}
			if int64(len(content)) > remaining {
				return nil, nil, errSkillUploadTooLarge
			}
			totalBytes += int64(len(content))
			files = append(files, uploadedSkillFile{Path: fileName, Content: content})
		default:
			_ = part.Close()
			return nil, nil, errors.New("invalid multipart field: " + fieldName)
		}
	}
	if len(displayTitleValues) > 1 {
		return nil, nil, errors.New("invalid multipart field: display_title")
	}
	if len(files) == 0 {
		return nil, nil, errors.New("files are required")
	}
	if len(displayTitleValues) == 0 {
		return nil, files, nil
	}
	return optionalTrimmedString(displayTitleValues[0]), files, nil
}

func skillMultipartPartDisposition(part *multipart.Part) (string, string, error) {
	if part == nil {
		return "", "", errors.New("multipart part is required")
	}
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return "", "", err
	}
	fieldName := strings.TrimSpace(params["name"])
	if fieldName == "" {
		return "", "", errors.New("multipart field name is required")
	}
	return fieldName, params["filename"], nil
}

func writeSkillUploadError(c *gin.Context, err error) {
	if errors.Is(err, errSkillUploadTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
}

func optionalTrimmedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
