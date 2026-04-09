package audit

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for entropy audit/lab endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a new audit Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes registers the audit routes on the given router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/quantum-entropy/audit", h.RunAudit)
}

// RunAudit godoc - GET /api/v1/quantum-entropy/audit?size=8192
func (h *Handler) RunAudit(c *gin.Context) {
	size := 8192 // default (Standard)
	if s := c.Query("size"); s != "" {
		if parsed, err := strconv.Atoi(s); err == nil {
			size = parsed
		}
	}

	report, err := h.service.RunFullAudit(size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}
