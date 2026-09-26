package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// Recovery catches panics in handlers, logs the panic with a stack trace
// and returns a clean JSON 500 instead of crashing the process.
// It replaces gin's built-in Recovery to demonstrate the defer/recover pattern.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() { // defer — runs when the handler chain returns (or panics)
			if r := recover(); r != nil { // recover — captures the panic value
				slog.Error("Panic recovered",
					"error", r,
					"path", c.Request.URL.Path,
					"stack", string(debug.Stack()))
				c.AbortWithStatusJSON(http.StatusInternalServerError,
					gin.H{"error": "internal server error"})
			}
		}()
		c.Next() // proceed down the handler chain (where a panic may occur)
	}
}