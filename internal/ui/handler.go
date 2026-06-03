package ui

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/leporoni/quantum-entropy-go-service/internal/audit"
	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
)

const poolMax = 1000

// Handler serves HTMX HTML fragments for the cyberpunk frontend.
type Handler struct {
	svc      *keymanager.Service
	repo     *keymanager.Repository
	auditSvc *audit.Service
}

// NewHandler creates a new UI Handler.
func NewHandler(svc *keymanager.Service, repo *keymanager.Repository, auditSvc *audit.Service) *Handler {
	return &Handler{svc: svc, repo: repo, auditSvc: auditSvc}
}

// RegisterRoutes registers all UI routes on the Gin engine.
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.Static("/static", "./web/static")
	r.GET("/", func(c *gin.Context) {
		c.File("./web/static/index.html")
	})

	ui := r.Group("/ui")
	ui.GET("/pool-status", h.poolStatus)
	ui.GET("/keys", h.listKeys)
	ui.POST("/keys", h.generateKey)
	ui.DELETE("/keys", h.deleteAllKeys)
	ui.DELETE("/keys/:id", h.deleteKey)
	ui.POST("/keys/:id/export", h.exportKey)
	ui.GET("/audit", h.runAudit)
}

// GET /ui/pool-status — entropy pool card fragment
func (h *Handler) poolStatus(c *gin.Context) {
	count, _ := h.repo.CountAllUnusedEntropy()
	pct := int(math.Min(float64(count)/float64(poolMax)*100, 100))

	color := "var(--neon-cyan)"
	if count < 200 {
		color = "#ff0050"
	} else if count < 500 {
		color = "var(--neon-yellow)"
	}

	c.Data(http.StatusOK, "text/html", []byte(fmt.Sprintf(`
<h2>// ENTROPY POOL</h2>
<div class="pool-header">
  <span class="pool-count">%d</span>
  <span class="pool-max">/ %d records</span>
</div>
<div class="entropy-bar">
  <div class="entropy-fill" style="width:%d%%; background: linear-gradient(90deg, %s, var(--neon-green))"></div>
</div>
<p style="margin-top:0.75rem;font-size:0.8rem;color:var(--text-secondary)">
  Each record = 256 bytes of real quantum entropy &bull; %.1f KB available
</p>`, count, poolMax, pct, color, float64(count)*256/1024)))
}

// GET /ui/keys — keys table fragment
func (h *Handler) listKeys(c *gin.Context) {
	keys, err := h.repo.FindAllKeys()
	if err != nil || len(keys) == 0 {
		c.Data(http.StatusOK, "text/html", []byte(`
<div class="empty-state">No keys in vault. Generate one above.</div>`))
		return
	}

	var sb strings.Builder
	sb.WriteString(`<table class="keys-table">
<thead><tr>
  <th>ID</th><th>Alias</th><th>Key Size</th><th>Created</th><th>Actions</th>
</tr></thead><tbody>`)

	for _, k := range keys {
		sb.WriteString(fmt.Sprintf(`<tr>
  <td class="key-id">#%d</td>
  <td class="key-alias">%s</td>
  <td>%d bits</td>
  <td>%s</td>
  <td class="actions">
    <button class="btn-secondary btn-sm"
            hx-post="/ui/keys/%d/export"
            hx-target="#export-modal-%d"
            hx-swap="innerHTML">
      📤 Export
    </button>
    <button class="btn-secondary btn-sm btn-danger"
            hx-delete="/ui/keys/%d"
            hx-target="closest tr"
            hx-swap="outerHTML"
            hx-confirm="Delete key '%s'?">
      🗑
    </button>
  </td>
</tr>
<tr id="export-modal-%d"><td colspan="5" style="padding:0"></td></tr>`,
			k.ID, k.Alias, k.KeySize,
			k.CreatedAt.Format(time.RFC3339),
			k.ID, k.ID, k.ID, k.Alias, k.ID))
	}

	sb.WriteString(`</tbody></table>`)
	c.Data(http.StatusOK, "text/html", []byte(sb.String()))
}

// POST /ui/keys — generate key, return updated list
func (h *Handler) generateKey(c *gin.Context) {
	alias := strings.TrimSpace(c.PostForm("alias"))
	keySize, _ := strconv.Atoi(c.PostForm("keySize"))
	if keySize == 0 {
		keySize = 2048
	}

	if _, err := h.svc.GenerateKey(alias, keySize); err != nil {
		c.Data(http.StatusServiceUnavailable, "text/html", []byte(fmt.Sprintf(`
<div class="empty-state" style="color:#ff0050">❌ %s</div>`, err.Error())))
		return
	}

	h.listKeys(c)
}

// DELETE /ui/keys — delete all, return empty state
func (h *Handler) deleteAllKeys(c *gin.Context) {
	h.repo.DeleteAllKeys()
	c.Data(http.StatusOK, "text/html", []byte(`
<div class="empty-state">No keys in vault. Generate one above.</div>`))
}

// DELETE /ui/keys/:id — delete one, remove row (swap outerHTML)
func (h *Handler) deleteKey(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	h.repo.DeleteKeyByID(id)
	c.Data(http.StatusOK, "text/html", []byte("")) // remove row
}

// POST /ui/keys/:id/export — show private key PEM inline
func (h *Handler) exportKey(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	pem, err := h.svc.ExportPrivateKey(id)
	if err != nil {
		c.Data(http.StatusOK, "text/html", []byte(fmt.Sprintf(`
<td colspan="5"><div class="empty-state" style="color:#ff0050">❌ %s</div></td>`, err.Error())))
		return
	}

	c.Data(http.StatusOK, "text/html", []byte(fmt.Sprintf(`
<td colspan="5">
  <div style="padding:1rem;background:var(--bg-secondary);border-radius:6px;margin:0.5rem 1rem">
    <div style="display:flex;justify-content:space-between;margin-bottom:0.5rem">
      <span style="font-family:var(--font-mono);font-size:0.75rem;color:var(--neon-magenta)">// PRIVATE KEY (AES-256-GCM UNWRAPPED)</span>
      <button class="btn-secondary btn-sm"
              onclick="navigator.clipboard.writeText(this.closest('td').querySelector('pre').textContent)">
        📋 Copy
      </button>
    </div>
    <pre style="font-family:var(--font-mono);font-size:0.72rem;color:var(--text-secondary);white-space:pre-wrap;word-break:break-all;max-height:200px;overflow-y:auto">%s</pre>
  </div>
</td>`, string(pem))))
}

// GET /ui/audit — entropy audit results fragment
func (h *Handler) runAudit(c *gin.Context) {
	size, _ := strconv.Atoi(c.Query("size"))
	if size <= 0 {
		size = 8192
	}

	report, err := h.auditSvc.RunFullAudit(size)
	if err != nil || len(report.Results) == 0 {
		msg := "No quantum data in pool yet. Wait for pool to fill."
		if err != nil {
			msg = err.Error()
		}
		c.Data(http.StatusOK, "text/html", []byte(fmt.Sprintf(`
<div class="empty-state">⚠️ %s</div>`, msg)))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<p style="color:var(--text-secondary);font-size:0.8rem;margin-bottom:1rem">Sample size: %d bytes</p>`, report.SampleSize))
	sb.WriteString(`<div class="audit-grid">`)

	for _, r := range report.Results {
		sb.WriteString(fmt.Sprintf(`
<div class="audit-card">
  <h3>%s</h3>
  <div class="metric-row"><span class="metric-label">Shannon Entropy</span><span class="metric-value">%.3f bits/byte</span></div>
  <div class="metric-row"><span class="metric-label">Chi-Square</span><span class="metric-value">%.2f</span></div>
  <div class="metric-row"><span class="metric-label">Pi Estimate</span><span class="metric-value">%.4f</span></div>
  <div class="metric-row"><span class="metric-label">Compression Ratio</span><span class="metric-value">%.4f</span></div>
  <div class="metric-row"><span class="metric-label">Repetitions</span><span class="metric-value">%d</span></div>
</div>`,
			r.Source, r.ShannonEntropy, r.ChiSquare, r.PiEstimate, r.CompressionRatio, r.Repetitions))
	}

	sb.WriteString(`</div>`)
	c.Data(http.StatusOK, "text/html", []byte(sb.String()))
}

func parseID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id), err
}
