package keymanager

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Handler exposes the keymanager REST API via Gin.
type Handler struct {
	svc  *Service
	repo *Repository
}

// NewHandler creates a new Handler.
func NewHandler(svc *Service, repo *Repository) *Handler {
	return &Handler{svc: svc, repo: repo}
}

// RegisterRoutes registers all keymanager routes on the given RouterGroup.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	keys := rg.Group("/keys")
	keys.POST("", h.generateKey)
	keys.GET("", h.listKeys)
	keys.DELETE("/:id", h.deleteKey)
	keys.DELETE("", h.deleteAllKeys)
	keys.POST("/:id/export", h.exportKey)

	entropy := rg.Group("/quantum-entropy")
	entropy.GET("/status", h.poolStatus)
}

// POST /keys
func (h *Handler) generateKey(c *gin.Context) {
	var req struct {
		Alias   string `json:"alias" binding:"required"`
		KeySize int    `json:"keySize"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.KeySize == 0 {
		req.KeySize = 2048
	}

	key, err := h.svc.GenerateKey(req.Alias, req.KeySize)
	if err != nil {
		status := http.StatusInternalServerError
		if err.Error() == "pool exhausted: insufficient entropy in pool" {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, toKeyResponse(key))
}

// GET /keys
func (h *Handler) listKeys(c *gin.Context) {
	keys, err := h.repo.FindAllKeys()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp := make([]keyResponse, len(keys))
	for i, k := range keys {
		resp[i] = toKeyResponse(&k)
	}
	c.JSON(http.StatusOK, resp)
}

// DELETE /keys/:id
func (h *Handler) deleteKey(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.repo.DeleteKeyByID(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// DELETE /keys
func (h *Handler) deleteAllKeys(c *gin.Context) {
	if err := h.repo.DeleteAllKeys(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// POST /keys/:id/export
func (h *Handler) exportKey(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	privPEM, err := h.svc.ExportPrivateKey(id)
	if err != nil {
		status := http.StatusInternalServerError
		if err.Error() == "key not found" {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"privateKeyPEM": string(privPEM)})
}

// GET /quantum-entropy/status
func (h *Handler) poolStatus(c *gin.Context) {
	count, err := h.svc.PoolStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"availableRecords": count})
}

// --- helpers ---

type keyResponse struct {
	ID           uint   `json:"id"`
	Alias        string `json:"alias"`
	KeySize      int    `json:"keySize"`
	PublicKeyPEM string `json:"publicKeyPEM"`
}

func toKeyResponse(k *RsaKey) keyResponse {
	return keyResponse{
		ID:           k.ID,
		Alias:        k.Alias,
		KeySize:      k.KeySize,
		PublicKeyPEM: k.PublicKeyPEM,
	}
}

func parseID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id), err
}
