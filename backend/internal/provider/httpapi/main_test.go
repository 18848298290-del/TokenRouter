package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin mode is a process-wide setting used by HTTP fixtures, including parallel
// test entries. Setting TestMode once before m.Run gives all provider HTTP
// tests the same initial test-mode configuration before their execution begins.

// TestMain selects Gin's test mode before executing the package test suite. The
// single global assignment precedes serial and parallel fixtures, giving them
// TestMode as their initial process-wide Gin setting.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
