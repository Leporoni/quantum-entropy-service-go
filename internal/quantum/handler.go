package quantum

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests for quantum entropy endpoints.
type Handler struct {
	service *Service
}

// NewHandler creates a new quantum Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes registers the quantum routes on the given router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/quantum-random", h.GetQuantumData)
}

// GetQuantumData godoc
// GET /api/v1/quantum-random?source=LFD&count=128&pure=false
func (h *Handler) GetQuantumData(c *gin.Context) {
	count := 128 // default
	pure := false // default

	if c.Query("count") != "" {
		// TODO: parse count from query
	}
	if c.Query("pure") == "true" {
		pure = true
	}

	base64Data, err := h.service.GetEntropyAsBase64(count, pure)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": base64Data})
}
