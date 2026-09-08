package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/leporoni/quantum-entropy-go-service/internal/audit"
	"github.com/leporoni/quantum-entropy-go-service/internal/keymanager"
)

const poolMax = 1000

var httpClient = &http.Client{Timeout: 3 * time.Second}

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
	ui.GET("/system-status", h.systemStatus)
	ui.GET("/rabbitmq-queues", h.rabbitmqQueues)
	ui.GET("/keys", h.listKeys)
	ui.POST("/keys", h.generateKey)
	ui.DELETE("/keys", h.deleteAllKeys)
	ui.DELETE("/keys/:id", h.deleteKey)
	ui.POST("/keys/:id/export", h.exportKey)
	ui.GET("/audit", h.runAudit)
	ui.GET("/lab", h.runLab)
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
</tr></thead>`)

	for _, k := range keys {
		sb.WriteString(fmt.Sprintf(`<tbody id="key-row-%d"><tr>
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
            hx-target="#key-row-%d"
            hx-swap="outerHTML"
            hx-confirm="Delete key '%s'?">
      🗑
    </button>
  </td>
</tr>
<tr id="export-modal-%d"><td colspan="5" style="padding:0"></td></tr></tbody>`,
			k.ID,
			k.ID, k.Alias, k.KeySize,
			k.CreatedAt.Format(time.RFC3339),
			k.ID, k.ID, k.ID, k.ID, k.Alias, k.ID))
	}

	sb.WriteString(`</table>`)
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
	h.svc.DeleteAllKeys()
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
	h.svc.DeleteKey(id)
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

// GET /ui/lab?suite=basic|min-entropy|nist|structure&size=N&seed=S — lab suite fragment
func (h *Handler) runLab(c *gin.Context) {
	suite := c.Query("suite")
	if suite == "" {
		suite = "basic"
	}
	size, _ := strconv.Atoi(c.Query("size"))
	if size <= 0 {
		size = 8192
	}
	seed, _ := strconv.ParseInt(c.Query("seed"), 10, 64)

	result, err := h.auditSvc.RunSuites(suite, size, seed)
	if err != nil {
		c.Data(http.StatusOK, "text/html", []byte(fmt.Sprintf(`
<div class="empty-state">⚠️ %s</div>`, err.Error())))
		return
	}
	if len(result.Results) == 0 {
		c.Data(http.StatusOK, "text/html", []byte(`
<div class="empty-state">⚠️ No quantum data in pool yet. Wait for pool to fill.</div>`))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`
<div class="lab-meta">
  <div><span class="lab-meta-label">SUITE</span><span class="lab-meta-value">%s</span></div>
  <div><span class="lab-meta-label">SAMPLE SIZE</span><span class="lab-meta-value">%d bytes</span></div>
  <div><span class="lab-meta-label">MIN REC</span><span class="lab-meta-value">%s</span></div>
  <div><span class="lab-meta-label">DESCRIPTION</span><span class="lab-meta-value">%s</span></div>
</div>`, result.Name, result.SampleSize, result.MinNote, result.Description))

	if result.Indicative {
		sb.WriteString(`
<div class="lab-banner">⚠️ Sample below the suite's recommended minimum — result is <strong>indicative</strong>, not a formal pass/fail.</div>`)
	}

	if suite == "basic" {
		sb.WriteString(`<div class="audit-grid">`)
		for _, sr := range result.Results {
			sb.WriteString(fmt.Sprintf(`
<div class="audit-card">
  <h3>%s</h3>`, sr.Source))
			for _, m := range sr.Metrics {
				sb.WriteString(fmt.Sprintf(`
  <div class="metric-row"><span class="metric-label">%s</span><span class="metric-value">%s <span class="verdict verdict-%s">%s</span></span></div>`,
					m.Name, m.Value, m.Verdict, verdictLabel(m.Verdict)))
			}
			sb.WriteString(`
</div>`)
		}
		sb.WriteString(`</div>`)
	} else {
		sb.WriteString(`<table class="lab-table">
<thead><tr><th style="width:22%%">Source</th><th>Metric</th><th>Value</th><th>Reference</th><th>Verdict</th></tr></thead><tbody>`)
		for _, sr := range result.Results {
			sb.WriteString(fmt.Sprintf(`<tr class="lab-source-row"><td colspan="5" class="lab-source">▸ %s</td></tr>`, sr.Source))
			for _, m := range sr.Metrics {
				sb.WriteString(fmt.Sprintf(`<tr>
  <td></td>
  <td>%s</td>
  <td>%s</td>
  <td>%s</td>
  <td><span class="verdict verdict-%s">%s</span></td>
</tr>`, m.Name, m.Value, m.Reference, m.Verdict, verdictLabel(m.Verdict)))
			}
		}
		sb.WriteString(`</tbody></table>`)
	}

	c.Data(http.StatusOK, "text/html", []byte(sb.String()))
}

// verdictLabel renders a Verdict as an uppercase label for the UI.
func verdictLabel(v audit.Verdict) string {
	switch v {
	case audit.VerdictPass:
		return "PASS"
	case audit.VerdictWarn:
		return "WARN"
	default:
		return "FAIL"
	}
}

// GET /ui/system-status — system status card fragment (checks services server-side)
func (h *Handler) systemStatus(c *gin.Context) {
	quantumAPIURL := getEnv("API_BASE_URL", "http://quantum-api:8081") + "/health"
	rabbitmqURL := "http://" + getEnv("RABBITMQ_MGMT_HOST", "rabbitmq:15672") + "/api/overview"

	quantumBadge := checkService(quantumAPIURL, "", "")
	rabbitmqBadge := checkService(rabbitmqURL, "guest", "guest")

	c.Data(http.StatusOK, "text/html", []byte(fmt.Sprintf(`
<h2>// SYSTEM STATUS</h2>
<div class="status-row">
  <span class="status-label">Quantum API</span>%s
</div>
<div class="status-row" style="margin-top:0.75rem">
  <span class="status-label">Key Manager</span>
  <span class="badge badge-online">online</span>
</div>
<div class="status-row" style="margin-top:0.75rem">
  <span class="status-label">RabbitMQ</span>%s
</div>`, quantumBadge, rabbitmqBadge)))
}

// checkService performs a GET to url and returns an HTML badge.
func checkService(url, user, pass string) string {
	req, _ := http.NewRequest("GET", url, nil)
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := httpClient.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return `<span class="badge badge-offline">offline</span>`
	}
	return `<span class="badge badge-online">online</span>`
}

// GET /ui/rabbitmq-queues — RabbitMQ queues fragment via Management API
func (h *Handler) rabbitmqQueues(c *gin.Context) {
	host := getEnv("RABBITMQ_MGMT_HOST", "rabbitmq:15672")
	url := "http://" + host + "/api/queues"

	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("guest", "guest")

	resp, err := httpClient.Do(req)
	if err != nil {
		c.Data(http.StatusOK, "text/html", []byte(`
<div class="empty-state">⚠️ Cannot reach RabbitMQ Management API.</div>`))
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var queues []struct {
		Name      string `json:"name"`
		Messages  int    `json:"messages"`
		Ready     int    `json:"messages_ready"`
		Unacked   int    `json:"messages_unacknowledged"`
		Consumers int    `json:"consumers"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(body, &queues); err != nil || len(queues) == 0 {
		c.Data(http.StatusOK, "text/html", []byte(`
<div class="empty-state">No queues found.</div>`))
		return
	}

	var sb strings.Builder
	sb.WriteString(`<table class="keys-table">
<thead><tr>
  <th>Queue</th><th>State</th><th>Ready</th><th>Unacked</th><th>Total</th><th>Consumers</th>
</tr></thead><tbody>`)

	for _, q := range queues {
		stateClass := "badge-online"
		if q.State != "running" {
			stateClass = "badge-offline"
		}
		sb.WriteString(fmt.Sprintf(`<tr>
  <td class="key-alias">%s</td>
  <td><span class="badge %s">%s</span></td>
  <td class="key-id">%d</td>
  <td class="key-id">%d</td>
  <td style="color:var(--neon-cyan);font-family:var(--font-mono)">%d</td>
  <td class="key-id">%d</td>
</tr>`, q.Name, stateClass, q.State, q.Ready, q.Unacked, q.Messages, q.Consumers))
	}
	sb.WriteString(`</tbody></table>`)
	c.Data(http.StatusOK, "text/html", []byte(sb.String()))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id), err
}
