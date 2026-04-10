package managedagents

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *Handler) CreateSkill(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	displayTitle := optionalTrimmedString(c.PostForm("display_title"))
	files, err := readUploadedSkillFiles(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	skill, err := h.service.CreateSkill(c.Request.Context(), principal, displayTitle, files)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, skill)
}

func (h *Handler) ListSkills(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	items, nextPage, hasMore, err := h.service.ListSkills(c.Request.Context(), principal, limit, c.Query("page"), c.Query("source"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "next_page": nextPage, "has_more": hasMore})
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
	c.JSON(http.StatusOK, skill)
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
	c.JSON(http.StatusOK, response)
}

func (h *Handler) CreateSkillVersion(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	files, err := readUploadedSkillFiles(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	version, err := h.service.CreateSkillVersion(c.Request.Context(), principal, c.Param("skill_id"), files)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, version)
}

func (h *Handler) ListSkillVersions(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	items, nextPage, hasMore, err := h.service.ListSkillVersions(c.Request.Context(), principal, c.Param("skill_id"), limit, c.Query("page"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "next_page": nextPage, "has_more": hasMore})
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
	c.JSON(http.StatusOK, version)
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
	c.JSON(http.StatusOK, response)
}

func readUploadedSkillFiles(c *gin.Context) ([]uploadedSkillFile, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
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

func optionalTrimmedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
