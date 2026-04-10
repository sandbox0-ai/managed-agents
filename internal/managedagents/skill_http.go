package managedagents

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
	"go.uber.org/zap"
)

func (h *Handler) CreateSkill(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	displayTitle, files, err := readValidatedSkillCreateUpload(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	skill, err := h.service.CreateSkill(c.Request.Context(), principal, displayTitle, files)
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
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	files, err := readValidatedSkillVersionUpload(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	version, err := h.service.CreateSkillVersion(c.Request.Context(), principal, c.Param("skill_id"), files)
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

func readUploadedSkillFiles(c *gin.Context) ([]uploadedSkillFile, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	return uploadedSkillFilesFromForm(form)
}

func readValidatedSkillCreateUpload(c *gin.Context) (*string, []uploadedSkillFile, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, nil, err
	}
	if err := validateSkillMultipartForm(form, true); err != nil {
		return nil, nil, err
	}
	displayTitle, err := singleOptionalMultipartValue(form, "display_title")
	if err != nil {
		return nil, nil, err
	}
	files, err := uploadedSkillFilesFromForm(form)
	if err != nil {
		return nil, nil, err
	}
	return displayTitle, files, nil
}

func readValidatedSkillVersionUpload(c *gin.Context) ([]uploadedSkillFile, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	if err := validateSkillMultipartForm(form, false); err != nil {
		return nil, err
	}
	return uploadedSkillFilesFromForm(form)
}

func uploadedSkillFilesFromForm(form *multipart.Form) ([]uploadedSkillFile, error) {
	if form == nil {
		return nil, errors.New("multipart form is required")
	}
	fileHeaders := form.File["files"]
	if len(fileHeaders) == 0 {
		return nil, errors.New("files are required")
	}
	files := make([]uploadedSkillFile, 0, len(fileHeaders))
	for _, header := range fileHeaders {
		file, err := header.Open()
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(file)
		file.Close()
		if readErr != nil {
			return nil, readErr
		}
		files = append(files, uploadedSkillFile{Path: header.Filename, Content: content})
	}
	return files, nil
}

func validateSkillMultipartForm(form *multipart.Form, allowDisplayTitle bool) error {
	if form == nil {
		return errors.New("multipart form is required")
	}
	for key := range form.Value {
		if key == "display_title" && allowDisplayTitle {
			continue
		}
		return errors.New("invalid multipart field: " + key)
	}
	for key := range form.File {
		if key != "files" {
			return errors.New("invalid multipart field: " + key)
		}
	}
	if !allowDisplayTitle {
		if values := form.Value["display_title"]; len(values) > 0 {
			return errors.New("invalid multipart field: display_title")
		}
	}
	return nil
}

func singleOptionalMultipartValue(form *multipart.Form, key string) (*string, error) {
	if form == nil {
		return nil, nil
	}
	values := form.Value[key]
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 1 {
		return nil, errors.New("invalid multipart field: " + key)
	}
	return optionalTrimmedString(values[0]), nil
}

func optionalTrimmedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
